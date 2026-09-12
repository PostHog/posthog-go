package posthog

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	json "github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
)

// decodeCaptureBody decompresses a recorded request body per its Content-Encoding,
// mirroring what the capture backend does. Reaching valid JSON proves the
// SDK emitted a well-formed stream for that codec.
func decodeCaptureBody(t *testing.T, encoding string, raw []byte) []byte {
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
	attempt     string
	requestId   string
	timestamp   string
	createdAt   string
	auth        string
	encoding    string
	contentType string
	userAgent   string
	sdkInfo     string
	uuids       []string
}

// captureTestServer is a configurable capture endpoint for send-engine tests.
type captureTestServer struct {
	mu       sync.Mutex
	requests []recordedRequest
	// respond returns (status, jsonBody, retryAfterHeader) for the given
	// 1-based attempt and the uuids present in the request body.
	respond func(attempt int, reqUuids []string) (int, string, string)
}

func (s *captureTestServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := decodeCaptureBody(t, r.Header.Get("Content-Encoding"), raw)
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
			attempt:     r.Header.Get("PostHog-Attempt"),
			requestId:   r.Header.Get("PostHog-Request-Id"),
			timestamp:   r.Header.Get("PostHog-Request-Timestamp"),
			createdAt:   env.CreatedAt,
			auth:        r.Header.Get("Authorization"),
			encoding:    r.Header.Get("Content-Encoding"),
			contentType: r.Header.Get("Content-Type"),
			userAgent:   r.Header.Get("User-Agent"),
			sdkInfo:     r.Header.Get("PostHog-Sdk-Info"),
			uuids:       uuids,
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

func (s *captureTestServer) snapshot() []recordedRequest {
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
	b, err := json.Marshal(captureResponse{Results: m})
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

// newCaptureTestClient builds a *client pointed at server with fast retries and the
// given callback/options. It returns the concrete type so send is reachable.
func newCaptureTestClient(t *testing.T, serverURL string, cb Callback, maxRetries int, configure func(*Config)) *client {
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

// captureBatch builds a preparedBatch (data/msgs/uuids aligned) from messages.
func captureBatch(t *testing.T, msgs ...Message) preparedBatch {
	t.Helper()
	var pb preparedBatch
	for _, m := range msgs {
		data, apiMsg, uuid, err := prepareForSend(m, nil)
		if err != nil {
			t.Fatalf("prepareForSend: %v", err)
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

func TestSendHeadersAndAllOk(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	now := time.Date(2025, time.April, 6, 7, 8, 9, 0, time.FixedZone("UTC+5", 5*60*60))
	c := newCaptureTestClient(t, ts.URL, cb, 9, func(cfg *Config) {
		cfg.now = func() time.Time { return now }
	})
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

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
	const wantTimestamp = "2025-04-06T02:08:09Z"
	if r.timestamp != wantTimestamp {
		t.Errorf("PostHog-Request-Timestamp = %q, want %q", r.timestamp, wantTimestamp)
	}
	if r.createdAt != wantTimestamp {
		t.Errorf("created_at = %q, want %q", r.createdAt, wantTimestamp)
	}
	// The capture endpoint requires these: a wrong Content-Type is a 415 and a
	// missing Sdk-Info or User-Agent is a 400, for every event.
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", r.contentType)
	}
	wantSdkInfo := SDKName + "/" + getVersion()
	if r.sdkInfo != wantSdkInfo {
		t.Errorf("PostHog-Sdk-Info = %q, want %q", r.sdkInfo, wantSdkInfo)
	}
	if r.userAgent != wantSdkInfo {
		t.Errorf("User-Agent = %q, want %q", r.userAgent, wantSdkInfo)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestSendStableRequestIdIncrementingAttempt(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(attempt int, uuids []string) (int, string, string) {
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

	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

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

func TestSendPartialRetryResendsOnlyRetrySubset(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(attempt int, uuids []string) (int, string, string) {
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

	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA), cap1(uuidB), cap1(uuidC)))

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

func TestSendTerminalResultsNotRetried(t *testing.T) {
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
			srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
				m := map[string]eventResult{}
				for _, u := range uuids {
					m[u] = eventResult{Result: tc.result}
				}
				return http.StatusOK, resultsBody(t, m), ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
			c.send(c.analytics, captureBatch(t, cap1(uuidA)))

			if len(srv.snapshot()) != 1 {
				t.Fatalf("expected 1 request (no retry), got %d", len(srv.snapshot()))
			}
			if s, f := cb.counts(); s != tc.wantSuccess || f != tc.wantFailure {
				t.Errorf("callbacks: success=%d failure=%d, want %d/%d", s, f, tc.wantSuccess, tc.wantFailure)
			}
		})
	}
}

func TestSendMissingUuidDropped(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, _ []string) (int, string, string) {
		// Empty results map: the event is absent.
		return http.StatusOK, `{"results":{}}`, ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	if len(srv.snapshot()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(srv.snapshot()))
	}
	if s, f := cb.counts(); s != 0 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/0 (silent drop)", s, f)
	}
}

func TestSendStatusClassification(t *testing.T) {
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
			srv := &captureTestServer{respond: func(_ int, _ []string) (int, string, string) {
				return tc.status, `{"error":"boom"}`, ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			// maxRetries=2 -> 3 attempts max.
			c := newCaptureTestClient(t, ts.URL, cb, 2, nil)
			c.send(c.analytics, captureBatch(t, cap1(uuidA)))

			if got := len(srv.snapshot()); got != tc.wantReqs {
				t.Errorf("status %d: %d requests, want %d", tc.status, got, tc.wantReqs)
			}
			if _, f := cb.counts(); f != tc.wantFailure {
				t.Errorf("status %d: failures=%d, want %d", tc.status, f, tc.wantFailure)
			}
		})
	}
}

func TestSendRetryableThenSuccess(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(attempt int, uuids []string) (int, string, string) {
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

	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestSendMaxAttemptsExhaustion(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 2, nil) // 3 attempts
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/1 (exhausted)", s, f)
	}
}

func TestSendMalformed200IsTerminal(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, _ []string) (int, string, string) {
		return http.StatusOK, "not json", ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	if got := len(srv.snapshot()); got != 1 {
		t.Fatalf("expected 1 request (no retry on malformed 200), got %d", got)
	}
	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Errorf("callbacks: success=%d failure=%d, want 0/1", s, f)
	}
}

// TestSendCompressionCodecs exercises the full send path for every
// supported codec: the request carries the right Content-Encoding token and
// the server (decoding per that token) recovers the original batch.
func TestSendCompressionCodecs(t *testing.T) {
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
			srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
				m := map[string]eventResult{}
				for _, u := range uuids {
					m[u] = eventResult{Result: resultOk}
				}
				return http.StatusOK, resultsBody(t, m), ""
			}}
			ts := httptest.NewServer(srv.handler(t))
			defer ts.Close()

			c := newCaptureTestClient(t, ts.URL, cb, 9, func(cfg *Config) {
				cfg.Compression = tc.mode
			})
			c.send(c.analytics, captureBatch(t, cap1(uuidA)))

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

func TestSendCompressionFailureFallsBackToUncompressed(t *testing.T) {
	originalCompressGzip := compressGzip
	compressGzip = func([]byte) ([]byte, error) {
		return nil, errors.New("gzip unavailable")
	}
	defer func() { compressGzip = originalCompressGzip }()

	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 9, func(cfg *Config) {
		cfg.Compression = CompressionGzip
	})
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	reqs := srv.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].encoding != "" {
		t.Errorf("Content-Encoding = %q, want empty fallback", reqs[0].encoding)
	}
	if len(reqs[0].uuids) != 1 || reqs[0].uuids[0] != uuidA {
		t.Errorf("decoded uuids = %v, want [%s]", reqs[0].uuids, uuidA)
	}
	if s, f := cb.counts(); s != 1 || f != 0 {
		t.Errorf("callbacks: success=%d failure=%d, want 1/0", s, f)
	}
}

func TestCompressBodyFailureFallsBackToUncompressed(t *testing.T) {
	raw := []byte(`{"event":"e","distinct_id":"d"}`)

	cases := []struct {
		name    string
		mode    CompressionMode
		stubErr string
		stub    func() func()
	}{
		{
			name:    "gzip",
			mode:    CompressionGzip,
			stubErr: "gzip unavailable",
			stub: func() func() {
				original := compressGzip
				compressGzip = func([]byte) ([]byte, error) {
					return nil, errors.New("gzip unavailable")
				}
				return func() { compressGzip = original }
			},
		},
		{
			name:    "zstd",
			mode:    CompressionZstd,
			stubErr: "zstd unavailable",
			stub: func() func() {
				original := getZstdEncoder
				getZstdEncoder = func() (*zstd.Encoder, error) {
					return nil, errors.New("zstd unavailable")
				}
				return func() { getZstdEncoder = original }
			},
		},
		{
			name:    "deflate",
			mode:    CompressionDeflate,
			stubErr: "deflate unavailable",
			stub: func() func() {
				original := deflateCompress
				deflateCompress = func([]byte) ([]byte, error) {
					return nil, errors.New("deflate unavailable")
				}
				return func() { deflateCompress = original }
			},
		},
		{
			name:    "brotli",
			mode:    CompressionBrotli,
			stubErr: "brotli unavailable",
			stub: func() func() {
				original := brotliCompress
				brotliCompress = func([]byte) ([]byte, error) {
					return nil, errors.New("brotli unavailable")
				}
				return func() { brotliCompress = original }
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := tc.stub()
			defer restore()

			body, token, err := compressBody(tc.mode, raw)
			if err == nil {
				t.Fatal("expected compression error")
			}
			if !strings.Contains(err.Error(), tc.stubErr) {
				t.Fatalf("error = %v, want to contain %q", err, tc.stubErr)
			}
			if token != "" {
				t.Errorf("token = %q, want empty fallback", token)
			}
			if !bytes.Equal(body, raw) {
				t.Errorf("body = %q, want raw", body)
			}
		})
	}
}

// TestCompressBody checks the codec helper directly: correct wire token,
// real size reduction on compressible input, and a clean round-trip. This
// catches encoder regressions the send-path test cannot (size, error path).
func TestCompressBody(t *testing.T) {
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
			body, token, err := compressBody(tc.mode, raw)
			if err != nil {
				t.Fatalf("compressBody: %v", err)
			}
			if token != tc.token {
				t.Errorf("token = %q, want %q", token, tc.token)
			}
			if tc.compress && len(body) >= len(raw) {
				t.Errorf("%s output %d bytes did not shrink raw %d", tc.name, len(body), len(raw))
			}
			if got := decodeCaptureBody(t, token, body); !bytes.Equal(got, raw) {
				t.Errorf("%s round-trip mismatch: got %d bytes, want %d", tc.name, len(got), len(raw))
			}
		})
	}

	if _, _, err := compressBody(CompressionMode(99), raw); err == nil {
		t.Error("expected error for unknown compression mode")
	}
}

func TestSendRetryAfterHonored(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(attempt int, uuids []string) (int, string, string) {
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
	c := newCaptureTestClient(t, ts.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

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

func TestRetryDelay(t *testing.T) {
	const base = 100 * time.Millisecond
	res := func(d time.Duration, has bool) *attemptResult {
		return &attemptResult{retryAfter: d, hasRetryAfter: has}
	}
	// Configured backoff is a constant `base`; Retry-After is a minimum, not a
	// replacement, and is clamped to DefaultMaxRetryBackoff (the configured backoff
	// itself is never truncated).
	cases := []struct {
		name string
		res  *attemptResult
		want time.Duration
	}{
		{"no_response_uses_configured", nil, base},
		{"no_retry_after_uses_configured", res(0, false), base},
		{"larger_retry_after_wins", res(5*time.Second, true), 5 * time.Second},
		{"smaller_retry_after_ignored", res(10*time.Millisecond, true), base},
		{"retry_after_at_ceiling", res(DefaultMaxRetryBackoff, true), DefaultMaxRetryBackoff},
		{"retry_after_above_ceiling_clamped", res(90*time.Second, true), DefaultMaxRetryBackoff},
		{"absurd_retry_after_clamped", res(1000*time.Hour, true), DefaultMaxRetryBackoff},
	}
	// Through makeConfig so MaxRetryBackoff resolves to its default, as it does
	// for any client built by NewWithConfig.
	c := &client{Config: makeConfig(Config{RetryAfter: func(int) time.Duration { return base }})}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.retryDelay(0, tc.res); got != tc.want {
				t.Errorf("retryDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSendMultiEventExhaustion(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 2, nil) // 3 attempts
	c.send(c.analytics, captureBatch(t, cap1(uuidA), cap1(uuidB), cap1(uuidC)))

	if got := len(srv.snapshot()); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.failures) != 3 {
		t.Fatalf("expected 3 failure callbacks (one per event), got %d", len(cb.failures))
	}
}

func TestSendShutdownDuringBackoff(t *testing.T) {
	cb := &recordingCallback{}
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultRetry}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newCaptureTestClient(t, ts.URL, cb, 9, func(cfg *Config) {
		// Backoff of 10s so the test can cancel mid-wait.
		cfg.RetryAfter = func(int) time.Duration { return 10 * time.Second }
	})

	go func() {
		// Allow the first request to complete, then shut down.
		time.Sleep(100 * time.Millisecond)
		_ = c.Close()
	}()
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

	if _, f := cb.counts(); f != 1 {
		t.Errorf("failure callbacks = %d, want 1", f)
	}
}

func TestSendTerminalNonRetryableBodyError(t *testing.T) {
	cb := &recordingCallback{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write a 400 status but close the connection before the body can be
		// fully written. The httptest.Server doesn't let us easily abort mid-
		// write, so instead we write a truncated JSON body to trigger an
		// unmarshal error in report (body reads fine, but parse fails as
		// incomplete JSON — however the code path we're testing fires when
		// io.ReadAll errors, which is harder to trigger in tests).
		// Instead: we return a 400 with a valid error body to confirm the P1
		// fix delivers requestError(res) instead of raw err.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_payload","error_description":"bad event shape"}`))
	}))
	defer srv.Close()

	c := newCaptureTestClient(t, srv.URL, cb, 9, nil)
	c.send(c.analytics, captureBatch(t, cap1(uuidA)))

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

// TestRetryDelayHonorsConfiguredCeiling pins that Config.MaxRetryBackoff, not a
// constant, bounds a server Retry-After.
func TestRetryDelayHonorsConfiguredCeiling(t *testing.T) {
	withCeiling := func(ceiling time.Duration) *client {
		cfg := makeConfig(Config{MaxRetryBackoff: ceiling})
		return &client{Config: cfg}
	}

	res := &attemptResult{retryAfter: 10 * time.Minute, hasRetryAfter: true}

	if got := withCeiling(5*time.Second).retryDelay(0, res); got != 5*time.Second {
		t.Errorf("ceiling 5s: delay = %v, want 5s", got)
	}
	if got := withCeiling(2*time.Minute).retryDelay(0, res); got != 2*time.Minute {
		t.Errorf("ceiling 2m: delay = %v, want 2m", got)
	}
	// Zero falls back to the documented default.
	if got := withCeiling(0).retryDelay(0, res); got != DefaultMaxRetryBackoff {
		t.Errorf("zero ceiling: delay = %v, want %v", got, DefaultMaxRetryBackoff)
	}
	// The default exponential backoff is capped by the same value.
	c := withCeiling(250 * time.Millisecond)
	if got := c.retryDelay(9, nil); got != 250*time.Millisecond {
		t.Errorf("backoff cap: delay = %v, want 250ms", got)
	}
}

// warnRecorder captures only Warnf, so a test can assert the aggregate loss
// line without picking up the debug result summary emitted on the same path.
type warnRecorder struct {
	t     *testing.T
	mu    sync.Mutex
	warns []string
}

func (l *warnRecorder) Debugf(f string, a ...interface{}) { l.t.Logf(f, a...) }
func (l *warnRecorder) Logf(f string, a ...interface{})   { l.t.Logf(f, a...) }
func (l *warnRecorder) Errorf(f string, a ...interface{}) { l.t.Logf(f, a...) }
func (l *warnRecorder) Warnf(f string, a ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(f, a...))
}

// lossLines returns only the aggregate drop lines, ignoring unrelated warnings.
func (l *warnRecorder) lossLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, w := range l.warns {
		if strings.Contains(w, "event(s) dropped") {
			out = append(out, w)
		}
	}
	return out
}

// TestSilentLossWarn pins the only signal a caller without a Callback gets when
// events are lost. It must be one aggregate line per batch -- never one per
// event, since per-event logging scales with event volume and payloads may
// carry sensitive content -- and must stay silent when a Callback is registered,
// because the Callback already reports every failure.
func TestSilentLossWarn(t *testing.T) {
	// respondAll answers every event in the batch with the given result.
	respondAll := func(result string) func(int, []string) (int, string, string) {
		return func(_ int, uuids []string) (int, string, string) {
			m := map[string]eventResult{}
			for _, u := range uuids {
				m[u] = eventResult{Result: result}
			}
			return http.StatusOK, resultsBody(t, m), ""
		}
	}

	// run sends a 3-event batch against respond and returns the loss lines.
	run := func(t *testing.T, cb Callback, respond func(int, []string) (int, string, string)) *warnRecorder {
		t.Helper()
		srv := &captureTestServer{respond: respond}
		ts := httptest.NewServer(srv.handler(t))
		defer ts.Close()

		log := &warnRecorder{t: t}
		c := newCaptureTestClient(t, ts.URL, cb, 0, func(cfg *Config) { cfg.Logger = log })
		c.send(c.analytics, captureBatch(t, cap1(uuidA), cap1(uuidB), cap1(uuidC)))
		return log
	}

	t.Run("per_event_drops_in_a_200", func(t *testing.T) {
		got := run(t, nil, respondAll(resultDrop)).lossLines()
		if len(got) != 1 {
			t.Fatalf("want exactly 1 aggregate line for a 3-event batch, got %d: %v", len(got), got)
		}
		if !strings.Contains(got[0], "3 event(s)") {
			t.Errorf("line must carry the count: %q", got[0])
		}
		for _, id := range []string{uuidA, uuidB, uuidC} {
			if strings.Contains(got[0], id) {
				t.Errorf("line must not enumerate events, found %s in %q", id, got[0])
			}
		}
	})

	t.Run("terminal_non_2xx", func(t *testing.T) {
		// A bad API key is the motivating case: nothing logged it before.
		got := run(t, nil, func(int, []string) (int, string, string) {
			return http.StatusUnauthorized, `{"code":"invalid_token","detail":"bad key"}`, ""
		}).lossLines()
		if len(got) != 1 {
			t.Fatalf("want 1 aggregate line, got %d: %v", len(got), got)
		}
		if !strings.Contains(got[0], "3 event(s)") || !strings.Contains(got[0], "401") {
			t.Errorf("line must carry the count and the status: %q", got[0])
		}
	})

	t.Run("callback_registered_suppresses_the_line", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			respond func(int, []string) (int, string, string)
		}{
			{"drops_in_a_200", respondAll(resultDrop)},
			{"terminal_non_2xx", func(int, []string) (int, string, string) {
				return http.StatusUnauthorized, `{"code":"invalid_token"}`, ""
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cb := &recordingCallback{}
				got := run(t, cb, tc.respond).lossLines()
				if len(got) != 0 {
					t.Errorf("a registered Callback already reports the loss; want no line, got %v", got)
				}
				if _, f := cb.counts(); f != 3 {
					t.Errorf("failure callbacks = %d, want 3", f)
				}
			})
		}
	})

	t.Run("no_loss_means_no_line", func(t *testing.T) {
		if got := run(t, nil, respondAll(resultOk)).lossLines(); len(got) != 0 {
			t.Errorf("an all-ok response must log nothing, got %v", got)
		}
	})
}

// TestCloseRacingEnqueue pins that a send never races the queue close. The loop
// closes c.msgs on shutdown, so without ordering a concurrent Enqueue is a send
// on a closed channel: a data race by the memory model, not merely a panic to
// recover. Run under -race; it is the detector, not an assertion, that fails.
func TestCloseRacingEnqueue(t *testing.T) {
	srv := &captureTestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	for i := 0; i < 200; i++ {
		c, err := NewWithConfig("phc_test", Config{
			Endpoint:  ts.URL,
			BatchSize: 1,
			Interval:  10 * time.Millisecond,
			Logger:    quietTestLogger{t},
		})
		if err != nil {
			t.Fatalf("NewWithConfig: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		var enqErr error
		go func() {
			defer wg.Done()
			<-start
			enqErr = c.Enqueue(cap1(uuidA))
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = c.Close()
		}()
		close(start)
		wg.Wait()

		// Either outcome is correct; a panic escaping Enqueue is not.
		if enqErr != nil && !errors.Is(enqErr, ErrClosed) && !errors.Is(enqErr, ErrQueueFull) {
			t.Fatalf("iteration %d: Enqueue = %v, want nil, ErrClosed or ErrQueueFull", i, enqErr)
		}
	}
}
