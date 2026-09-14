package posthog_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/require"
)

func TestFlushKeepsFailedEventsRetryable(t *testing.T) {
	for _, mode := range []posthog.CaptureMode{posthog.CaptureModeLegacy, posthog.CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			var requests atomic.Int32
			var received atomic.Int32
			releaseRetry := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseRetry) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				flushReply(t, w, r, func(events []string) { received.Add(int32(len(events))) })
			}))
			defer server.Close()
			callback := &flushCallbacks{}
			c, err := posthog.NewWithConfig("test-key", posthog.Config{
				Endpoint: server.URL, CaptureMode: mode, Interval: time.Hour,
				Callback: callback, ShutdownTimeout: time.Second,
				RetryAfter: func(int) time.Duration { <-releaseRetry; return 0 },
			})
			require.NoError(t, err)
			defer c.Close()
			defer release()
			f := c.(flushingClient)
			require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "user-123", Event: "Save"}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, f.FlushWithContext(ctx), "flush must return while the failed event is retained for retry")
			require.EqualValues(t, 1, requests.Load())
			require.Zero(t, received.Load())
			require.Zero(t, callback.failed.Load(), "retryable event must not be dropped")
			require.NoError(t, f.FlushWithContext(ctx), "another flush must respect pending retry backoff")
			require.EqualValues(t, 1, requests.Load())
			release()
			require.Eventually(t, func() bool { return received.Load() == 1 }, 5*time.Second, time.Millisecond,
				"the original event must still be available for a successful retry")
			require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "user-123", Event: "Later"}))
			later, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			require.NoError(t, f.FlushWithContext(later))
			require.EqualValues(t, 2, received.Load())
			require.Zero(t, callback.failed.Load())
		})
	}
}

func TestFlushWaitsForActiveRetry(t *testing.T) {
	for _, mode := range []posthog.CaptureMode{posthog.CaptureModeLegacy, posthog.CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			var requests atomic.Int32
			retryStarted := make(chan struct{})
			retryRelease := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(retryRelease) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(503)
					return
				}
				close(retryStarted)
				select {
				case <-retryRelease:
					flushReply(t, w, r)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			defer release()
			c, f := newFlushClient(t, server, mode)
			require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "retry"}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Start a cycle, but do not assume the caller is scheduled before retry.
			first := make(chan error, 1)
			go func() { first <- f.FlushWithContext(ctx) }()
			select {
			case <-retryStarted:
			case <-ctx.Done():
				t.Fatal("retry not started")
			}
			short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
			// A deadline is a guard for a deliberately blocked HTTP attempt.
			require.ErrorIs(t, f.FlushWithContext(short), context.DeadlineExceeded)
			stop()
			release()
			require.NoError(t, <-first)
			require.NoError(t, f.FlushWithContext(ctx))
		})
	}
}
