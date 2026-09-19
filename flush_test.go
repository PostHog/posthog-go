package posthog_test

import (
	"context"
	"encoding/json"
	"errors"
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

type flushingClient interface {
	Flush() error
	FlushWithContext(context.Context) error
}

func newFlushClient(t *testing.T, server *httptest.Server, mode posthog.CaptureMode) (posthog.Client, flushingClient) {
	t.Helper()
	c, err := posthog.NewWithConfig("test-key", posthog.Config{
		Endpoint: server.URL, CaptureMode: mode, Interval: time.Hour, BatchSize: 100,
		ShutdownTimeout: time.Second, RetryAfter: func(int) time.Duration { return time.Millisecond },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	f, ok := c.(flushingClient)
	require.True(t, ok, "client must expose reusable Flush and FlushWithContext")
	return c, f
}

func flushReply(t *testing.T, w http.ResponseWriter, r *http.Request, record ...func([]string)) []string {
	t.Helper()
	var body struct {
		Batch []struct {
			Event string `json:"event"`
			UUID  string `json:"uuid"`
		} `json:"batch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
		w.WriteHeader(400)
		return nil
	}
	events := make([]string, 0, len(body.Batch))
	results := map[string]interface{}{}
	for _, event := range body.Batch {
		events = append(events, event.Event)
		results[event.UUID] = map[string]string{"result": "ok"}
	}
	for _, observe := range record {
		observe(events)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"results": results}); err != nil {
		t.Error(err)
	}
	return events
}

func TestFlushReusable(t *testing.T) {
	for _, mode := range []posthog.CaptureMode{posthog.CaptureModeLegacy, posthog.CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			var mu sync.Mutex
			var received []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				received = append(received, flushReply(t, w, r)...)
			}))
			defer server.Close()
			c, f := newFlushClient(t, server, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, f.FlushWithContext(ctx))
			mu.Lock()
			require.Empty(t, received)
			mu.Unlock()
			for _, name := range []string{"first", "second"} {
				require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "user", Event: name}))
				require.NoError(t, f.FlushWithContext(ctx))
			}
			mu.Lock()
			require.Equal(t, []string{"first", "second"}, received)
			mu.Unlock()
			require.NoError(t, f.Flush())
			require.NoError(t, c.Close())
			require.ErrorIs(t, f.FlushWithContext(ctx), posthog.ErrClosed)
		})
	}
}

func TestFlushWaitsAndCancellationPreservesClient(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			flushReply(t, w, r)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer unblock()
	c, f := newFlushClient(t, server, posthog.CaptureModeLegacy)
	require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "held"}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- f.FlushWithContext(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not dispatch")
	}
	select {
	case err := <-result:
		t.Fatalf("flush returned before delivery: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not return")
	}
	require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "after cancellation"}))
	unblock()
	done, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	require.NoError(t, f.FlushWithContext(done))
}

func TestFlushConcurrentAndRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		flushReply(t, w, r)
	}))
	defer server.Close()
	c, f := newFlushClient(t, server, posthog.CaptureModeLegacy)
	require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "retry"}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.FlushWithContext(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.Eventually(t, func() bool { return requests.Load() == 2 }, time.Second, time.Millisecond)
}

func TestFlushDisabled(t *testing.T) {
	c := posthog.New("")
	f, ok := c.(flushingClient)
	require.True(t, ok)
	require.True(t, errors.Is(f.Flush(), posthog.ErrSDKDisabled))
	require.ErrorIs(t, f.FlushWithContext(context.Background()), posthog.ErrSDKDisabled)
}

func TestFlushCancelledContextAndConcurrentEnqueue(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushReply(t, w, r, func(events []string) { received.Add(int32(len(events))) })
	}))
	defer server.Close()
	c, f := newFlushClient(t, server, posthog.CaptureModeLegacy)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, f.FlushWithContext(cancelled), context.Canceled)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := c.Enqueue(posthog.Capture{DistinctId: "u", Event: "parallel"}); err != nil {
					t.Error(err)
				}
				if err := f.FlushWithContext(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	require.NoError(t, f.FlushWithContext(ctx))
	require.EqualValues(t, 100, received.Load())
	require.NoError(t, f.FlushWithContext(nil))
}

func TestFlushV1PartialRetry(t *testing.T) {
	var mu sync.Mutex
	var batches [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []struct {
				UUID string `json:"uuid"`
			} `json:"batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ids := make([]string, 0, len(body.Batch))
		results := map[string]interface{}{}
		for i, event := range body.Batch {
			ids = append(ids, event.UUID)
			outcome := "ok"
			if len(batches) == 0 && i == 1 {
				outcome = "retry"
			}
			results[event.UUID] = map[string]string{"result": outcome}
		}
		batches = append(batches, ids)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"results": results}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c, f := newFlushClient(t, server, posthog.CaptureModeAnalyticsV1)
	for _, event := range []string{"accepted", "retry"} {
		require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: event}))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.FlushWithContext(ctx))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(batches) == 2
	}, time.Second, time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, batches, 2)
	require.Len(t, batches[0], 2)
	require.Equal(t, []string{batches[0][1]}, batches[1], "retry only the requested event, preserving its UUID")
}

func TestFlushCycleDoesNotWaitForLaterBatches(t *testing.T) {
	firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	unblockFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	unblockSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-firstRelease:
			case <-r.Context().Done():
				return
			}
		} else {
			close(secondStarted)
			select {
			case <-secondRelease:
			case <-r.Context().Done():
				return
			}
		}
		flushReply(t, w, r)
	}))
	defer server.Close()
	defer unblockFirst()
	defer unblockSecond()
	c, f := newFlushClient(t, server, posthog.CaptureModeLegacy)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "first"}))
	first := make(chan error, 1)
	go func() { first <- f.FlushWithContext(ctx) }()
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("first batch not sent")
	}
	require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "second"}))
	second := make(chan error, 1)
	go func() { second <- f.FlushWithContext(ctx) }()
	select {
	case <-secondStarted:
	case <-ctx.Done():
		t.Fatal("second batch not sent")
	}
	unblockFirst()
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("first flush waited for later batch")
	}
	select {
	case err := <-second:
		t.Fatalf("second flush returned early: %v", err)
	default:
	}
	unblockSecond()
	require.NoError(t, <-second)
}

func TestFlushMultipleBatchesAndConcurrentClose(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushReply(t, w, r, func(events []string) { count.Add(int32(len(events))) })
	}))
	defer server.Close()
	c, f := newFlushClient(t, server, posthog.CaptureModeAnalyticsV1)
	for i := 0; i < 250; i++ {
		require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: fmt.Sprint(i)}))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.FlushWithContext(ctx))
	require.EqualValues(t, 250, count.Load())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := f.FlushWithContext(ctx)
			if err != nil && !errors.Is(err, posthog.ErrClosed) {
				t.Error(err)
			}
		}()
	}
	require.NoError(t, c.CloseWithContext(ctx))
	wg.Wait()
}

type flushCallbacks struct{ failed atomic.Int32 }

func (c *flushCallbacks) Success(posthog.APIMessage)        {}
func (c *flushCallbacks) Failure(posthog.APIMessage, error) { c.failed.Add(1) }

func TestFlushPreservesTerminalFailureCallbacks(t *testing.T) {
	for _, mode := range []posthog.CaptureMode{posthog.CaptureModeLegacy, posthog.CaptureModeAnalyticsV1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
			defer server.Close()
			callback := &flushCallbacks{}
			c, err := posthog.NewWithConfig("test-key", posthog.Config{Endpoint: server.URL, CaptureMode: mode, Interval: time.Hour, Callback: callback})
			require.NoError(t, err)
			defer c.Close()
			f := c.(flushingClient)
			require.NoError(t, c.Enqueue(posthog.Capture{DistinctId: "u", Event: "rejected"}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, f.FlushWithContext(ctx))
			require.EqualValues(t, 1, callback.failed.Load())
		})
	}
}
