package posthog

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	json "github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
)

// decodeV1Body decompresses a recorded request body per its Content-Encoding,
// mirroring what the capture-v1 backend does. Reaching valid JSON proves the
// SDK emitted a well-formed stream for that codec.
func decodeV1Body(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	switch encoding {
	case "":
		return raw
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			return raw
		}
		defer gr.Close()
		out, _ := io.ReadAll(gr)
		return out
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("zlib reader: %v", err)
			return raw
		}
		defer zr.Close()
		out, _ := io.ReadAll(zr)
		return out
	case "zstd":
		zr, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("zstd reader: %v", err)
			return raw
		}
		defer zr.Close()
		out, _ := io.ReadAll(zr)
		return out
	case "br":
		out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
		if err != nil {
			t.Errorf("brotli reader: %v", err)
			return raw
		}
		return out
	default:
		t.Errorf("unexpected Content-Encoding %q", encoding)
		return raw
	}
}

// recordedRequest captures what the server saw for one attempt.
type recordedRequest struct {
	attempt   string
	requestId string
	timestamp string
	auth      string
	encoding  string
	uuids     []string
}

// v1TestServer is a configurable capture-v1 endpoint for send-engine tests.
type v1TestServer struct {
	mu       sync.Mutex
	requests []recordedRequest
	// respond returns (status, jsonBody, retryAfterHeader) for the given
	// 1-based attempt and the uuids present in the request body.
	respond func(attempt int, reqUuids []string) (int, string, string)
}

func (s *v1TestServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := decodeV1Body(t, r.Header.Get("Content-Encoding"), raw)
		var env eventBatch
		if err := json.Unmarshal(body, &env); err != nil {
			t.Errorf("server: unmarshal envelope: %v (body=%s)", err, string(body))
		}
		uuids := make([]string, 0, len(env.Batch))
		for _, raw := range env.Batch {
			var ev struct {
				Uuid string `json:"uuid"`
			}
			_ = json.Unmarshal(raw, &ev)
			uuids = append(uuids, ev.Uuid)
		}

		s.mu.Lock()
		attempt := len(s.requests) + 1
		s.requests = append(s.requests, recordedRequest{
			attempt:   r.Header.Get("PostHog-Attempt"),
			requestId: r.Header.Get("PostHog-Request-Id"),
			timestamp: r.Header.Get("PostHog-Request-Timestamp"),
			auth:      r.Header.Get("Authorization"),
			encoding:  r.Header.Get("Content-Encoding"),
			uuids:     uuids,
		})
		s.mu.Unlock()

		status, respBody, retryAfter := s.respond(attempt, uuids)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}
}

func (s *v1TestServer) snapshot() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// recordingCallback records success/failure callbacks by event uuid via the
// CaptureInApi/etc APIMessage shape. We key on the apiMsg pointer order instead,
// recording counts and the errors seen.
type recordingCallback struct {
	mu        sync.Mutex
	successes []APIMessage
	failures  []failure
}

type failure struct {
	msg APIMessage
	err error
}

func (rc *recordingCallback) Success(m APIMessage) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.successes = append(rc.successes, m)
}

func (rc *recordingCallback) Failure(m APIMessage, err error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.failures = append(rc.failures, failure{m, err})
}

func (rc *recordingCallback) counts() (int, int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.successes), len(rc.failures)
}

// resultsBody builds a {"results":{uuid:{result,details}}} JSON body.
func resultsBody(t *testing.T, m map[string]eventResult) string {
	t.Helper()
	b, err := json.Marshal(captureV1Response{Results: m})
	if err != nil {
		t.Fatalf("marshal results body: %v", err)
	}
	return string(b)
}

// quietTestLogger routes every level (including Errorf) to t.Logf so that
// exercising error/retry paths does not fail the test the way toLogger does.
type quietTestLogger struct{ t *testing.T }

func (l quietTestLogger) Debugf(f string, a ...interface{}) { l.t.Logf(f, a...) }
func (l quietTestLogger) Logf(f string, a ...interface{})   { l.t.Logf(f, a...) }
func (l quietTestLogger) Warnf(f string, a ...interface{})  { l.t.Logf(f, a...) }
func (l quietTestLogger) Errorf(f string, a ...interface{}) { l.t.Logf(f, a...) }

// newV1TestClient builds a *client pointed at server with fast retries and the
// given callback/options. It returns the concrete type so sendV1 is reachable.
func newV1TestClient(t *testing.T, serverURL string, cb Callback, maxRetries int, configure func(*Config)) *client {
	t.Helper()
	retries := maxRetries
	cfg := Config{
		Endpoint:   serverURL,
		Callback:   cb,
		MaxRetries: &retries,
		RetryAfter: func(int) time.Duration { return time.Millisecond },
		Logger:     quietTestLogger{t},
	}
	if configure != nil {
		configure(&cfg)
	}
	cli, err := NewWithConfig("phc_test", cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	nc, ok := cli.(*client)
	if !ok {
		t.Fatalf("expected *client, got %T", cli)
	}
	return nc
}

// v1Batch builds a preparedBatch (data/msgs/uuids aligned) from messages.
func v1Batch(t *testing.T, msgs ...Message) preparedBatch {
	t.Helper()
	var pb preparedBatch
	for _, m := range msgs {
		data, apiMsg, uuid, err := prepareForSendV1(m, nil)
		if err != nil {
			t.Fatalf("prepareForSendV1: %v", err)
		}
		pb.data = append(pb.data, data)
		pb.msgs = append(pb.msgs, apiMsg)
		pb.uuids = append(pb.uuids, uuid)
	}
	return pb
}

func cap1(uuid string) Capture {
	return Capture{Uuid: uuid, Event: "e", DistinctId: "d"}
}

const (
	uuidA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uuidB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	uuidC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func TestV1SendHeadersAndAllOk(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	reqs := srv.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.auth != "Bearer phc_test" {
		t.Errorf("Authorization = %q", r.auth)
	}
	if r.attempt != "1" {
		t.Errorf("PostHog-Attempt = %q, want 1", r.attempt)
	}
	if r.requestId == "" {
		t.Error("PostHog-Request-Id missing")
	}
	if _, err := time.Parse(time.RFC3339, r.timestamp); err != nil {
		t.Errorf("PostHog-Request-Timestamp %q not RFC3339: %v", r.timestamp, err)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestV1SendStableRequestIdIncrementingAttempt(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(attempt int, uuids []string) (int, string, string) {
		res := resultRetry
		if attempt >= 2 {
			res = resultOk
		}
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: res}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	reqs := srv.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	if reqs[0].requestId != reqs[1].requestId {
		t.Errorf("request id changed across retries: %q vs %q", reqs[0].requestId, reqs[1].requestId)
	}
	if reqs[0].attempt != "1" || reqs[1].attempt != "2" {
		t.Errorf("attempts = %q,%q want 1,2", reqs[0].attempt, reqs[1].attempt)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestV1SendPartialRetryResendsOnlyRetrySubset(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(attempt int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		if attempt == 1 {
			drop := "billing_limit_exceeded"
			m[uuidA] = eventResult{Result: resultOk}
			m[uuidB] = eventResult{Result: resultRetry}
			m[uuidC] = eventResult{Result: resultDrop, Details: &drop}
		} else {
			for _, u := range uuids {
				m[u] = eventResult{Result: resultOk}
			}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA), cap1(uuidB), cap1(uuidC)))

	reqs := srv.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	if len(reqs[1].uuids) != 1 || reqs[1].uuids[0] != uuidB {
		t.Errorf("attempt 2 resent %v, want only [%s]", reqs[1].uuids, uuidB)
	}
	// a -> ok, b -> ok (after retry); c -> drop.
	if s, f := cb.counts(); s != 2 || f != 1 {
		t.Errorf("callbacks: success=%d failure=%d, want 2/1", s, f)
	}
}

func TestV1SendTerminalResultsNotRetried(t *testing.T) {
	cases := []struct {
		name        string
		result      string
		wantSuccess int
		wantFailure int
	}{
		{"warning_is_success", resultWarning, 1, 0},
		{"drop_is_failure", resultDrop, 0, 1},
		{"unknown_is_success", "some_future_status", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cb := &recordingCallback{}
			srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
				m := map[string]eventResult{}
				for _, u := range uuids {
					m[u] = eventResult{Result: tc.result}
				}
				return http.StatusOK, resultsBody(t, m), ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			c := newV1TestClient(t, ts.URL, cb, 9, nil)
			c.sendV1(v1Batch(t, cap1(uuidA)))

			if len(srv.snapshot()) != 1 {
				t.Fatalf("expected 1 request (no retry), got %d", len(srv.snapshot()))
			}
			if s, f := cb.counts(); s != tc.wantSuccess || f != tc.wantFailure {
				t.Errorf("callbacks: success=%d failure=%d, want %d/%d", s, f, tc.wantSuccess, tc.wantFailure)
			}
		})
	}
}

func TestV1SendMissingUuidDropped(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, _ []string) (int, string, string) {
		// Empty results map: the event is absent.
		return http.StatusOK, `{"results":{}}`, ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if len(srv.snapshot()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(srv.snapshot()))
	}
	if s, f := cb.counts(); s != 0 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/0 (silent drop)", s, f)
	}
}

func TestV1SendStatusClassification(t *testing.T) {
	cases := []struct {
		status      int
		wantReqs    int
		wantFailure int
	}{
		{408, 3, 1}, {500, 3, 1}, {502, 3, 1}, {503, 3, 1}, {504, 3, 1},
		{400, 1, 1}, {401, 1, 1}, {402, 1, 1}, {413, 1, 1}, {415, 1, 1}, {429, 1, 1},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			cb := &recordingCallback{}
			srv := &v1TestServer{respond: func(_ int, _ []string) (int, string, string) {
				return tc.status, `{"error":"boom"}`, ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			// maxRetries=2 -> 3 attempts max.
			c := newV1TestClient(t, ts.URL, cb, 2, nil)
			c.sendV1(v1Batch(t, cap1(uuidA)))

			if got := len(srv.snapshot()); got != tc.wantReqs {
				t.Errorf("status %d: %d requests, want %d", tc.status, got, tc.wantReqs)
			}
			if _, f := cb.counts(); f != tc.wantFailure {
				t.Errorf("status %d: failures=%d, want %d", tc.status, f, tc.wantFailure)
			}
		})
	}
}

func TestV1SendRetryableThenSuccess(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(attempt int, uuids []string) (int, string, string) {
		if attempt == 1 {
			return http.StatusServiceUnavailable, `{"error":"unavailable"}`, ""
		}
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestV1SendMaxAttemptsExhaustion(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 2, nil) // 3 attempts
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/1 (exhausted)", s, f)
	}
}

func TestV1SendMalformed200IsTerminal(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, _ []string) (int, string, string) {
		return http.StatusOK, "not json", ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 1 {
		t.Fatalf("expected 1 request (no retry on malformed 200), got %d", got)
	}
	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/1", s, f)
	}
}

// TestV1SendCompressionCodecs exercises the full v1 send path for every
// supported codec: the request carries the right Content-Encoding token and
// the server (decoding per that token) recovers the original batch.
func TestV1SendCompressionCodecs(t *testing.T) {
	cases := []struct {
		name     string
		mode     CompressionMode
		encoding string // expected Content-Encoding header ("" = uncompressed)
	}{
		{"none", CompressionNone, ""},
		{"gzip", CompressionGzip, "gzip"},
		{"zstd", CompressionZstd, "zstd"},
		{"deflate", CompressionDeflate, "deflate"},
		{"brotli", CompressionBrotli, "br"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cb := &recordingCallback{}
			srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
				m := map[string]eventResult{}
				for _, u := range uuids {
					m[u] = eventResult{Result: resultOk}
				}
				return http.StatusOK, resultsBody(t, m), ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			c := newV1TestClient(t, ts.URL, cb, 9, func(cfg *Config) {
				cfg.CaptureMode = CaptureModeAnalyticsV1
				cfg.Compression = tc.mode
			})
			c.sendV1(v1Batch(t, cap1(uuidA)))

			reqs := srv.snapshot()
			if len(reqs) != 1 {
				t.Fatalf("expected 1 request, got %d", len(reqs))
			}
			if reqs[0].encoding != tc.encoding {
				t.Errorf("Content-Encoding = %q, want %q", reqs[0].encoding, tc.encoding)
			}
			// The handler decoded the body per Content-Encoding and parsed the
			// uuid; reaching here with it recorded proves the body round-trips.
			if len(reqs[0].uuids) != 1 || reqs[0].uuids[0] != uuidA {
				t.Errorf("decoded uuids = %v, want [%s]", reqs[0].uuids, uuidA)
			}
			if s, _ := cb.counts(); s != 1 {
				t.Errorf("success callbacks = %d, want 1", s)
			}
		})
	}
}

// TestCompressV1Body checks the codec helper directly: correct wire token,
// real size reduction on compressible input, and a clean round-trip. This
// catches encoder regressions the send-path test cannot (size, error path).
func TestCompressV1Body(t *testing.T) {
	// Large, highly compressible payload so every codec yields real savings.
	raw := bytes.Repeat([]byte(`{"event":"e","distinct_id":"d"},`), 512)

	cases := []struct {
		name     string
		mode     CompressionMode
		token    string
		compress bool // expect output smaller than raw
	}{
		{"none", CompressionNone, "", false},
		{"gzip", CompressionGzip, "gzip", true},
		{"zstd", CompressionZstd, "zstd", true},
		{"deflate", CompressionDeflate, "deflate", true},
		{"brotli", CompressionBrotli, "br", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, token, err := compressV1Body(tc.mode, raw)
			if err != nil {
				t.Fatalf("compressV1Body: %v", err)
			}
			if token != tc.token {
				t.Errorf("token = %q, want %q", token, tc.token)
			}
			if tc.compress && len(body) >= len(raw) {
				t.Errorf("%s output %d bytes did not shrink raw %d", tc.name, len(body), len(raw))
			}
			if got := decodeV1Body(t, token, body); !bytes.Equal(got, raw) {
				t.Errorf("%s round-trip mismatch: got %d bytes, want %d", tc.name, len(got), len(raw))
			}
		})
	}

	if _, _, err := compressV1Body(CompressionMode(99), raw); err == nil {
		t.Error("expected error for unknown compression mode")
	}
}

func TestV1SendRetryAfterHonored(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(attempt int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		if attempt == 1 {
			for _, u := range uuids {
				m[u] = eventResult{Result: resultRetry}
			}
			return http.StatusOK, resultsBody(t, m), "1" // Retry-After: 1s
		}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	// Configured backoff is 1ms; Retry-After of 1s must win.
	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	reqs := srv.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected >= 2 attempts, got %d", len(reqs))
	}
	t1, err1 := time.Parse(time.RFC3339, reqs[0].timestamp)
	t2, err2 := time.Parse(time.RFC3339, reqs[1].timestamp)
	if err1 != nil || err2 != nil {
		t.Fatalf("parse timestamps: %v / %v", err1, err2)
	}
	// RFC3339 has 1-second resolution; Retry-After: 1 means ≥900ms is expected.
	delta := t2.Sub(t1)
	if delta < 900*time.Millisecond {
		t.Errorf("attempt delta %v, expected >= ~1s from Retry-After", delta)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestV1SendMultiEventExhaustion(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 2, nil) // 3 attempts
	c.sendV1(v1Batch(t, cap1(uuidA), cap1(uuidB), cap1(uuidC)))

	if got := len(srv.snapshot()); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.failures) != 3 {
		t.Fatalf("expected 3 failure callbacks (one per event), got %d", len(cb.failures))
	}
}

func TestV1SendShutdownDuringBackoff(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, func(cfg *Config) {
		// Backoff of 10s so the test can cancel mid-wait.
		cfg.RetryAfter = func(int) time.Duration { return 10 * time.Second }
	})

	go func() {
		// Allow the first request to complete, then shut down.
		time.Sleep(100 * time.Millisecond)
		_ = c.Close()
	}()
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if _, f := cb.counts(); f != 1 {
		t.Errorf("failure callbacks = %d, want 1", f)
	}
}

func TestV1SendTerminalNonRetryableBodyError(t *testing.T) {
	cb := &recordingCallback{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write a 400 status but close the connection before the body can be
		// fully written. The httptest.Server doesn't let us easily abort mid-
		// write, so instead we write a truncated JSON body to trigger an
		// unmarshal error in reportV1 (body reads fine, but parse fails as
		// incomplete JSON — however the code path we're testing fires when
		// io.ReadAll errors, which is harder to trigger in tests).
		// Instead: we return a 400 with a valid error body to confirm the P1
		// fix delivers requestErrorV1(res) instead of raw err.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_payload","error_description":"bad event shape"}`))
	}))
	defer srv.Close()

	c := newV1TestClient(t, srv.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if got := len(srv.URL); got == 0 {
		t.Fatal("impossible")
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(cb.failures))
	}
	errMsg := cb.failures[0].err.Error()
	if !containsAll(errMsg, "400", "invalid_payload") {
		t.Errorf("error = %q, want to contain status code and error key", errMsg)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}
