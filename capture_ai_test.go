package posthog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

// laneRecorder answers both capture paths and records which one each request
// hit, so a test can assert lane isolation in both directions.
type laneRecorder struct {
	mu       sync.Mutex
	paths    []string
	encoding map[string]string
	bodies   map[string][]byte
}

func newLaneRecorder() *laneRecorder {
	return &laneRecorder{encoding: map[string]string{}, bodies: map[string][]byte{}}
}

func (r *laneRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.encoding[req.URL.Path] = req.Header.Get("Content-Encoding")
		r.bodies[req.URL.Path] = body
		r.mu.Unlock()
		// Decode so an all-ok body can be built even when compressed.
		writeCaptureOK(w, decodeCaptureBody(t, req.Header.Get("Content-Encoding"), body))
	}))
}

func (r *laneRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *laneRecorder) encodingFor(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.encoding[path]
}

func (r *laneRecorder) bodyFor(path string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[path]
}

func (r *laneRecorder) count(path string) int {
	n := 0
	for _, p := range r.snapshot() {
		if p == path {
			n++
		}
	}
	return n
}

func aiTestClient(t *testing.T, url string, configure func(*Config)) Client {
	t.Helper()
	cfg := Config{
		Endpoint:  url,
		BatchSize: 1,
		Interval:  10 * time.Millisecond,
		Logger:    quietTestLogger{t},
	}
	if configure != nil {
		configure(&cfg)
	}
	c, err := NewWithConfig("phc_test", cfg)
	require.NoError(t, err)
	return c
}

// TestLaneIsolation pins both directions: EnqueueAI reaches only the AI
// endpoint, and Enqueue never reroutes an AI-named event onto it. The second
// half is canonical cross-SDK behavior -- customers send custom $ai_*-prefixed
// names as ordinary analytics events at volume.
func TestLaneIsolation(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	c := aiTestClient(t, srv.URL, nil)
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Enqueue(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Close())

	require.Equal(t, 1, rec.count(aiCapturePath), "EnqueueAI must reach the AI endpoint exactly once")
	require.Equal(t, 1, rec.count(capturePath), "Enqueue must stay on the analytics endpoint")
}

// TestAILaneNotStartedUnlessUsed pins the lazy start: a client that never calls
// EnqueueAI must not create the lane at all.
func TestAILaneNotStartedUnlessUsed(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	c := aiTestClient(t, srv.URL, nil)
	require.NoError(t, c.Enqueue(Capture{DistinctId: "d", Event: "ordinary"}))

	require.Nil(t, c.(*client).ai.Load(), "the AI lane must not exist before it is used")
	require.NoError(t, c.Close())
	require.Zero(t, rec.count(aiCapturePath))

	// And it is created once it is used.
	c2 := aiTestClient(t, srv.URL, nil)
	require.NoError(t, c2.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_span"}))
	require.NotNil(t, c2.(*client).ai.Load(), "the AI lane must exist after EnqueueAI")
	require.NoError(t, c2.Close())
}

// TestAILaneUsesItsOwnCompression pins that the analytics codec never applies
// to the AI lane and vice versa.
func TestAILaneUsesItsOwnCompression(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	c := aiTestClient(t, srv.URL, func(cfg *Config) {
		cfg.Compression = CompressionGzip
		cfg.CaptureAICompression = CompressionZstd
	})
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Enqueue(Capture{DistinctId: "d", Event: "ordinary"}))
	require.NoError(t, c.Close())

	require.Equal(t, "zstd", rec.encodingFor(aiCapturePath), "AI lane must use CaptureAICompression")
	require.Equal(t, "gzip", rec.encodingFor(capturePath), "analytics lane must use Compression")
}

// TestAILaneDefaultsToUncompressed pins that leaving CaptureAICompression unset
// sends raw bodies even when the analytics lane compresses.
func TestAILaneDefaultsToUncompressed(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	c := aiTestClient(t, srv.URL, func(cfg *Config) { cfg.Compression = CompressionGzip })
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Close())

	require.Empty(t, rec.encodingFor(aiCapturePath), "AI bodies default to uncompressed")
}

// TestAILaneSizeLimits pins that the AI lane carries its own byte limits rather
// than the analytics ones, which are far too small for AI payloads.
func TestAILaneSizeLimits(t *testing.T) {
	analytics := analyticsLaneConfig(makeConfig(Config{}))
	ai := aiLaneConfig(makeConfig(Config{}))

	require.Equal(t, DefaultMaxEventBytes, analytics.maxEventBytes)
	require.Equal(t, aiMaxEventBytes, ai.maxEventBytes)
	require.Equal(t, aiBatchBytesTarget, ai.maxBatchBytes)
	require.Greater(t, ai.maxEventBytes, analytics.maxEventBytes,
		"an AI event well over the analytics cap must still be accepted")

	require.Equal(t, DefaultCaptureAIMaxQueueSize, ai.maxQueueSize)
	require.Equal(t, DefaultMaxQueueSize, analytics.maxQueueSize)
	require.Equal(t, aiCapturePath, ai.path)
	require.Equal(t, capturePath, analytics.path)
}

// TestAILaneAcceptsEventOverAnalyticsCap is the behavioral half of the limits:
// a payload larger than the analytics per-event cap reaches the AI endpoint.
// Production AI events routinely exceed that cap.
func TestAILaneAcceptsEventOverAnalyticsCap(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	var failures []error
	var mu sync.Mutex
	c := aiTestClient(t, srv.URL, func(cfg *Config) {
		cfg.Callback = testCallback{nil, func(_ APIMessage, e error) {
			mu.Lock()
			failures = append(failures, e)
			mu.Unlock()
		}}
	})

	big := NewProperties().Set("blob", strings.Repeat("x", DefaultMaxEventBytes+1000))
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation", Properties: big}))
	require.NoError(t, c.Close())

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, failures, "an event over the analytics cap must not be rejected on the AI lane")
	require.Equal(t, 1, rec.count(aiCapturePath))
}

// TestAILaneDropsEventOverItsOwnCeiling pins the local pre-drop: an event past
// the backend's ai_event_too_big limit is refused before a doomed multi-megabyte
// upload is attempted.
func TestAILaneDropsEventOverItsOwnCeiling(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	var failures []error
	var mu sync.Mutex
	c := aiTestClient(t, srv.URL, func(cfg *Config) {
		cfg.Callback = testCallback{nil, func(_ APIMessage, e error) {
			mu.Lock()
			failures = append(failures, e)
			mu.Unlock()
		}}
	})

	huge := NewProperties().Set("blob", strings.Repeat("x", aiMaxEventBytes+1024))
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation", Properties: huge}))
	require.NoError(t, c.Close())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, failures, 1)
	require.ErrorIs(t, failures[0], ErrMessageTooBig)
	require.Zero(t, rec.count(aiCapturePath), "no request should be attempted for a pre-dropped event")
}

// TestCloseDrainsBothLanes pins that Close flushes the AI lane too, not just
// analytics.
func TestCloseDrainsBothLanes(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	// A long interval means nothing flushes on a timer: only Close can deliver.
	c := aiTestClient(t, srv.URL, func(cfg *Config) {
		cfg.BatchSize = 100
		cfg.Interval = time.Hour
	})
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Enqueue(Capture{DistinctId: "d", Event: "ordinary"}))
	require.NoError(t, c.Close())

	require.Equal(t, 1, rec.count(aiCapturePath), "Close must flush the AI lane")
	require.Equal(t, 1, rec.count(capturePath), "Close must flush the analytics lane")
}

// TestEnqueueAIAfterCloseReturnsErrClosed pins that a late call neither panics
// nor silently starts an undrained lane.
func TestEnqueueAIAfterCloseReturnsErrClosed(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	c := aiTestClient(t, srv.URL, nil)
	require.NoError(t, c.Close())
	require.ErrorIs(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"}), ErrClosed)
}

// TestCloseRacingFirstEnqueueAI pins the ordering hazard posthog-rs hit twice:
// Close marks the client closed before reading the AI lane, and the lane
// re-checks after starting, so a lane started in that window is either drained
// by Close or declines the message. Either way Close must return and no
// goroutine may be left running.
func TestCloseRacingFirstEnqueueAI(t *testing.T) {
	rec := newLaneRecorder()
	srv := rec.server(t)
	defer srv.Close()

	for i := 0; i < 100; i++ {
		c := aiTestClient(t, srv.URL, nil)

		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			// Either outcome is correct; what must not happen is a panic, a
			// hang, or a lane nobody drains.
			_ = c.EnqueueAI(Capture{DistinctId: "d", Event: "$ai_generation"})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = c.Close()
		}()
		close(start)

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: Close and EnqueueAI deadlocked", i)
		}

		// If a lane was started it must have finished draining.
		if l := c.(*client).ai.Load(); l != nil {
			select {
			case <-l.shutdown:
			case <-time.After(10 * time.Second):
				t.Fatalf("iteration %d: AI lane started but never drained", i)
			}
		}
	}
}

// TestCaptureErrorsCarryTheEndpoint pins the lane discriminator: one Callback
// can tell which lane a failure came from via errors.As.
func TestCaptureErrorsCarryTheEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		uuids := uuidsOf(t, body)
		results := map[string]eventResult{}
		for _, u := range uuids {
			results[u] = eventResult{Result: resultDrop, Details: ptrString("misrouted_event")}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resultsBody(t, results)))
	}))
	defer srv.Close()

	seen := map[string]string{}
	var mu sync.Mutex
	c := aiTestClient(t, srv.URL, func(cfg *Config) {
		cfg.Callback = testCallback{nil, func(_ APIMessage, e error) {
			var ee *CaptureEventError
			if errors.As(e, &ee) {
				mu.Lock()
				seen[ee.Endpoint] = ee.Details
				mu.Unlock()
			}
		}}
	})
	require.NoError(t, c.EnqueueAI(Capture{DistinctId: "d", Event: "not_an_ai_event"}))
	require.NoError(t, c.Enqueue(Capture{DistinctId: "d", Event: "$ai_generation"}))
	require.NoError(t, c.Close())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "misrouted_event", seen[aiCapturePath], "AI-lane drop must name the AI endpoint")
	require.Equal(t, "misrouted_event", seen[capturePath], "analytics-lane drop must name the analytics endpoint")
}

// uuidsOf pulls the event uuids out of a capture request envelope.
func uuidsOf(t *testing.T, body []byte) []string {
	t.Helper()
	var env eventBatch
	require.NoError(t, json.Unmarshal(body, &env))
	out := make([]string, 0, len(env.Batch))
	for _, raw := range env.Batch {
		var ev struct {
			Uuid string `json:"uuid"`
		}
		require.NoError(t, json.Unmarshal(raw, &ev))
		out = append(out, ev.Uuid)
	}
	return out
}

// aiOnlyClient implements Client's EnqueueAI but not the concrete
// EnqueueAIWithContext method, exercising the duck-type fallback in the
// package-level helper.
type aiOnlyClient struct {
	Client
	got chan Message
}

func (c *aiOnlyClient) EnqueueAI(msg Message) error {
	c.got <- msg
	return nil
}

// TestEnqueueAIWithContext covers the package-level helper: it must enrich from
// the request context exactly as EnqueueWithContext does, reach the AI endpoint
// and only the AI endpoint, and degrade to EnqueueAI (never the analytics lane)
// for a Client lacking the context-aware method.
func TestEnqueueAIWithContext(t *testing.T) {
	t.Run("enriches_from_context_and_hits_the_ai_endpoint", func(t *testing.T) {
		rec := newLaneRecorder()
		srv := rec.server(t)
		defer srv.Close()

		c := aiTestClient(t, srv.URL, nil)
		req := httptest.NewRequest(http.MethodPost, "https://example.com/llm", nil)
		req.Header.Set("x-posthog-distinct-id", "ctx-user")
		req.Header.Set("x-posthog-session-id", "ctx-session")
		ctx := WithFreshRequestContext(context.Background(), ExtractRequestContext(req, true))

		require.NoError(t, EnqueueAIWithContext(ctx, c, Capture{Event: "$ai_generation"}))
		require.NoError(t, c.Close())

		require.Equal(t, 1, rec.count(aiCapturePath), "must reach the AI endpoint exactly once")
		require.Zero(t, rec.count(capturePath), "must never reach the analytics endpoint")

		var env eventBatch
		require.NoError(t, json.Unmarshal(rec.bodyFor(aiCapturePath), &env))
		require.Len(t, env.Batch, 1)
		var ev map[string]interface{}
		require.NoError(t, json.Unmarshal(env.Batch[0], &ev))
		require.Equal(t, "ctx-user", ev["distinct_id"], "request-context distinct id must be applied")
		require.Equal(t, "ctx-session", ev["session_id"], "request-context session id must be applied")
	})

	t.Run("nil_client_errors", func(t *testing.T) {
		require.Error(t, EnqueueAIWithContext(context.Background(), nil, Capture{Event: "$ai_generation"}))
	})

	t.Run("falls_back_to_EnqueueAI_not_the_analytics_lane", func(t *testing.T) {
		fake := &aiOnlyClient{got: make(chan Message, 1)}
		require.NoError(t, EnqueueAIWithContext(context.Background(), fake, Capture{Event: "$ai_generation"}))

		select {
		case msg := <-fake.got:
			require.Equal(t, "$ai_generation", msg.(Capture).Event)
		default:
			t.Fatal("fallback must call EnqueueAI")
		}
	})
}
