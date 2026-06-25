package posthog

import (
	"net/http"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// waitForCounts polls cb until success+failure reach total or it times out.
func waitForCounts(t *testing.T, cb *UnifiedCallback, total int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		s, f := cb.GetCounts()
		if s+f >= total {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d callbacks, got success=%d failure=%d", total, s, f)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestV1ModeSelectionRoutesToV1Endpoint(t *testing.T) {
	b := NewMockServerBuilder()
	srv := b.Build()
	defer srv.Close()

	cb := NewUnifiedCallback(t)
	one := 0
	c, err := NewWithConfig("phc_test", Config{
		Endpoint:    srv.URL,
		CaptureMode: CaptureModeAnalyticsV1,
		Callback:    cb,
		MaxRetries:  &one,
		Logger:      quietTestLogger{t},
		Interval:    10 * time.Millisecond,
		BatchSize:   1,
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if err := c.Enqueue(Capture{Event: "e", DistinctId: "d"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitForCounts(t, cb, 1)
	_ = c.Close()

	paths := b.GetPaths()
	if len(paths) == 0 || paths[0] != captureV1Path {
		t.Errorf("v1 mode hit %v, want first request to %s", paths, captureV1Path)
	}
	if s, f := cb.GetCounts(); s != 1 || f != 0 {
		t.Errorf("callbacks success=%d failure=%d, want 1/0", s, f)
	}
}

func TestDefaultModeRoutesToBatchEndpoint(t *testing.T) {
	b := NewMockServerBuilder().WithBatchResponse("ok", 200)
	srv := b.Build()
	defer srv.Close()

	cb := NewUnifiedCallback(t)
	c, err := NewWithConfig("phc_test", Config{
		Endpoint:  srv.URL,
		Callback:  cb,
		Logger:    quietTestLogger{t},
		Interval:  10 * time.Millisecond,
		BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if err := c.Enqueue(Capture{Event: "e", DistinctId: "d"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitForCounts(t, cb, 1)
	_ = c.Close()

	for _, p := range b.GetPaths() {
		if p == captureV1Path {
			t.Errorf("default mode must not hit %s; paths=%v", captureV1Path, b.GetPaths())
		}
	}
}

func TestV1IntegrationEnvelopeAndHeaders(t *testing.T) {
	var gotAuth, gotSdkInfo string
	var envelope eventBatch
	srv := NewMockServerBuilder().WithCaptureV1Handler(func(body []byte) (int, string) {
		_ = json.Unmarshal(body, &envelope)
		return 200, allOkResultsBody(body)
	}).Build()
	defer srv.Close()

	cb := NewUnifiedCallback(t)
	c, _ := NewWithConfig("phc_test", Config{
		Endpoint:    srv.URL,
		CaptureMode: CaptureModeAnalyticsV1,
		Callback:    cb,
		Logger:      quietTestLogger{t},
		Interval:    10 * time.Millisecond,
		BatchSize:   1,
		Transport:   headerCaptureTransport{auth: &gotAuth, sdkInfo: &gotSdkInfo},
	})
	_ = c.Enqueue(Capture{Event: "e", DistinctId: "d"})
	waitForCounts(t, cb, 1)
	_ = c.Close()

	if envelope.CreatedAt == "" {
		t.Error("envelope created_at missing")
	}
	if len(envelope.Batch) != 1 {
		t.Errorf("envelope batch len = %d, want 1", len(envelope.Batch))
	}
	if gotAuth != "Bearer phc_test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotSdkInfo != SDKName+"/"+getVersion() {
		t.Errorf("PostHog-Sdk-Info = %q", gotSdkInfo)
	}
}

func TestV1IntegrationPartialRetryFiresPerEventCallbacks(t *testing.T) {
	attempts := 0
	b := NewMockServerBuilder().WithCaptureV1Handler(func(body []byte) (int, string) {
		attempts++
		var env eventBatch
		_ = json.Unmarshal(body, &env)
		results := map[string]eventResult{}
		for i, raw := range env.Batch {
			var ev struct {
				Uuid string `json:"uuid"`
			}
			_ = json.Unmarshal(raw, &ev)
			switch {
			case attempts == 1 && i == 0:
				results[ev.Uuid] = eventResult{Result: resultOk}
			case attempts == 1 && i == 1:
				drop := "billing_limit_exceeded"
				results[ev.Uuid] = eventResult{Result: resultDrop, Details: &drop}
			case attempts == 1 && i == 2:
				results[ev.Uuid] = eventResult{Result: resultRetry}
			default:
				results[ev.Uuid] = eventResult{Result: resultOk}
			}
		}
		out, _ := json.Marshal(captureV1Response{Results: results})
		return 200, string(out)
	})
	srv := b.Build()
	defer srv.Close()

	cb := NewUnifiedCallback(t)
	nine := 9
	c, _ := NewWithConfig("phc_test", Config{
		Endpoint:    srv.URL,
		CaptureMode: CaptureModeAnalyticsV1,
		Callback:    cb,
		MaxRetries:  &nine,
		RetryAfter:  func(int) time.Duration { return time.Millisecond },
		Logger:      quietTestLogger{t},
		Interval:    10 * time.Millisecond,
		BatchSize:   3,
	})
	_ = c.Enqueue(Capture{Uuid: uuidA, Event: "e1", DistinctId: "d"})
	_ = c.Enqueue(Capture{Uuid: uuidB, Event: "e2", DistinctId: "d"})
	_ = c.Enqueue(Capture{Uuid: uuidC, Event: "e3", DistinctId: "d"})

	// 3 events: a ok, b drop, c retry->ok = 2 success, 1 failure.
	waitForCounts(t, cb, 3)
	_ = c.Close()

	if s, f := cb.GetCounts(); s != 2 || f != 1 {
		t.Errorf("callbacks success=%d failure=%d, want 2/1", s, f)
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts (partial retry), got %d", attempts)
	}
}

// headerCaptureTransport records the auth + sdk-info headers then delegates to
// the default transport.
type headerCaptureTransport struct {
	auth    *string
	sdkInfo *string
}

func (t headerCaptureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.auth = r.Header.Get("Authorization")
	*t.sdkInfo = r.Header.Get("PostHog-Sdk-Info")
	return http.DefaultTransport.RoundTrip(r)
}
