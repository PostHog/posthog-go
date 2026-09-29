package posthog

import (
	"context"
	"net/http"
	"strings"

	json "github.com/goccy/go-json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newSlowBatchServer(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	t.Cleanup(server.Close)
	return server, &received
}

func enqueueTestCaptures(t *testing.T, client Client, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		err := client.Enqueue(Capture{DistinctId: "test-user", Event: "test-event"})
		require.NoError(t, err)
	}
}

func newBatchCounterServer(t *testing.T) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var batchCount atomic.Int64
	var totalMessages atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		batchCount.Add(1)
		totalMessages.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	t.Cleanup(server.Close)
	return server, &batchCount, &totalMessages
}

// TestBatching_SmallEventsBatchTogether verifies that small events are batched together
func TestBatching_SmallEventsBatchTogether(t *testing.T) {
	t.Parallel()

	server, batchCount, totalMessages := newBatchCounterServer(t)

	// Small events (~100 props, ~5KB each)
	// With 500KB batch limit, should fit ~100 events per batch
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: DefaultBatchSize,
		Interval:  5 * time.Second, // Long interval - rely on Close() to flush
	})
	require.NoError(t, err)

	// Send 50 small events - should all fit in one batch
	pool := NewEventPoolWithCardinality(50, CardinalityLow)
	for i := 0; i < 50; i++ {
		err := client.Enqueue(pool.Next())
		require.NoError(t, err)
	}

	client.Close()

	require.Equal(t, int64(50), totalMessages.Load(), "All 50 events should be delivered")
	// Small events should batch together efficiently
	require.LessOrEqual(t, batchCount.Load(), int64(3), "50 small events should fit in 3 or fewer batches")
}

// TestBatching_LargeEventsTriggerFlush verifies that large events trigger batch flushes
func TestBatching_LargeEventsTriggerFlush(t *testing.T) {
	t.Parallel()

	var batchCount atomic.Int64
	var batchSizes []int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		batchCount.Add(1)
		mu.Lock()
		batchSizes = append(batchSizes, len(b.Messages))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	// Ten ~100KB events exceed the byte limit but not the count limit.
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: DefaultBatchSize,
		Interval:  time.Hour,
	})
	require.NoError(t, err)

	defer client.Close()
	for i := 0; i < 10; i++ {
		require.NoError(t, client.Enqueue(Capture{
			DistinctId: "user", Event: "large",
			Properties: Properties{"payload": strings.Repeat("x", 100000)},
		}))
	}

	require.NoError(t, client.Close())
	require.Equal(t, int64(3), batchCount.Load())
	require.ElementsMatch(t, []int{4, 4, 2}, batchSizes)
}

// TestBatching_OversizedEventRejected verifies that events >500KB are rejected
func TestBatching_OversizedEventRejected(t *testing.T) {
	t.Parallel()

	var received atomic.Int64
	var failureCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	callback := &testCallbackCounter{
		onFailure: func() { failureCount.Add(1) },
	}

	client, err := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		Callback: callback,
	})
	require.NoError(t, err)

	// Create an event with MANY properties to exceed 500KB
	// Each property is ~50 bytes, need ~10000 properties to hit 500KB
	oversizedProps := make(Properties, 15000)
	for i := 0; i < 15000; i++ {
		oversizedProps[generateDistinctId(i)] = generateDistinctId(i + 100000)
	}

	err = client.Enqueue(Capture{
		DistinctId: "user_1",
		Event:      "oversized_event",
		Properties: oversizedProps,
	})
	require.NoError(t, err) // Enqueue itself doesn't fail

	client.Close()

	// The oversized event should be rejected via callback
	require.Equal(t, int64(0), received.Load(), "Oversized event should not be delivered")
	require.Equal(t, int64(1), failureCount.Load(), "Should have 1 failure callback for oversized event")
}

// TestBatching_MixedCardinalityBatching verifies correct batching with mixed event sizes
func TestBatching_MixedCardinalityBatching(t *testing.T) {
	t.Parallel()

	server, batchCount, totalMessages := newBatchCounterServer(t)

	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 250,
		Interval:  50 * time.Millisecond,
	})
	require.NoError(t, err)

	// Send mix of small and medium events
	smallPool := NewEventPoolWithCardinality(30, CardinalityLow)
	mediumPool := NewEventPoolWithCardinality(20, CardinalityMedium)

	// Interleave small and medium events
	for i := 0; i < 30; i++ {
		err := client.Enqueue(smallPool.Next())
		require.NoError(t, err)
		if i < 20 {
			err := client.Enqueue(mediumPool.Next())
			require.NoError(t, err)
		}
	}

	client.Close()

	require.Equal(t, int64(50), totalMessages.Load(), "All 50 events should be delivered")
	t.Logf("Batch count for mixed cardinality: %d", batchCount.Load())
}

// TestBatching_BatchCountLimit verifies BatchSize config is respected
func TestBatching_BatchCountLimit(t *testing.T) {
	t.Parallel()

	var batchSizes []int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		batchSizes = append(batchSizes, len(b.Messages))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	batchSize := 10
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: batchSize,
		Interval:  time.Hour,
	})
	require.NoError(t, err)

	defer client.Close()
	// Only the count limit or Close can flush these small events.
	pool := NewEventPoolWithCardinality(25, CardinalityLow)
	for i := 0; i < 25; i++ {
		err := client.Enqueue(pool.Next())
		require.NoError(t, err)
	}

	require.NoError(t, client.Close())
	require.ElementsMatch(t, []int{10, 10, 5}, batchSizes)
}

// testCallbackCounter is a simple callback for counting successes and failures
type testCallbackCounter struct {
	onSuccess func()
	onFailure func()
}

func (c *testCallbackCounter) Success(msg APIMessage) {
	if c.onSuccess != nil {
		c.onSuccess()
	}
}

func (c *testCallbackCounter) Failure(msg APIMessage, err error) {
	if c.onFailure != nil {
		c.onFailure()
	}
}

// TestConfigBatchSubmitTimeout verifies the default and custom BatchSubmitTimeout config
func TestConfigBatchSubmitTimeout(t *testing.T) {
	t.Parallel()

	// Test default value
	cfg := makeConfig(Config{})
	require.Equal(t, DefaultBatchSubmitTimeout, cfg.BatchSubmitTimeout, "default BatchSubmitTimeout should be %v", DefaultBatchSubmitTimeout)

	// Test custom value
	customTimeout := 200 * time.Millisecond
	cfg = makeConfig(Config{BatchSubmitTimeout: customTimeout})
	require.Equal(t, customTimeout, cfg.BatchSubmitTimeout, "custom BatchSubmitTimeout should be preserved")

	// Test negative value (non-blocking mode)
	cfg = makeConfig(Config{BatchSubmitTimeout: -1})
	require.Equal(t, time.Duration(-1), cfg.BatchSubmitTimeout, "negative BatchSubmitTimeout should be preserved")
}

func TestBatchSubmitTimeout_WaitsForWorkers(t *testing.T) {
	c := &client{
		Config:     Config{BatchSubmitTimeout: 5 * time.Second},
		batches:    make(chan preparedBatch, 1),
		deliveries: make(map[*delivery]struct{}),
	}
	processed := make(chan preparedBatch, 1)
	c.capture = batchRecordingCapturer{processed: processed}
	c.batches <- preparedBatch{}
	done := make(chan bool, 1)
	go func() { done <- c.sendBatch(preparedBatch{uuids: []string{"submitted"}}) }()
	require.Eventually(t, func() bool { return c.inFlight.Load() == 1 }, time.Second, time.Millisecond)
	select {
	case <-done:
		t.Fatal("submission returned before queue space was available")
	default:
	}
	<-c.batches
	select {
	case accepted := <-done:
		require.True(t, accepted)
	case <-time.After(5 * time.Second):
		t.Fatal("submission did not resume when queue space became available")
	}
	select {
	case batch := <-processed:
		require.Equal(t, []string{"submitted"}, batch.uuids)
	case <-time.After(5 * time.Second):
		t.Fatal("accepted batch was not processed")
	}
	require.Eventually(t, func() bool { return c.inFlight.Load() == 0 }, time.Second, time.Millisecond)
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	require.Empty(t, c.deliveries)
}

func TestBatchSubmitTimeout_FullQueue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{"nonblocking", -1},
		{"deadline", 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &client{
				Config:     Config{BatchSubmitTimeout: tc.timeout},
				batches:    make(chan preparedBatch, 1),
				deliveries: make(map[*delivery]struct{}),
			}
			c.batches <- preparedBatch{uuids: []string{"queued"}}
			done := make(chan bool, 1)
			go func() { done <- c.sendBatch(preparedBatch{}) }()
			select {
			case accepted := <-done:
				require.False(t, accepted)
			case <-time.After(time.Second):
				t.Fatal("submission blocked beyond its deadline")
			}
			require.Zero(t, c.inFlight.Load())
			require.Empty(t, c.deliveries)
			require.Equal(t, []string{"queued"}, (<-c.batches).uuids)
		})
	}
}

type batchRecordingCapturer struct {
	capturer
	processed chan<- preparedBatch
}

func (c batchRecordingCapturer) send(batch preparedBatch) { c.processed <- batch }

// TestShutdownTimeout_DefaultWaitsForCompletion verifies that with default config
// (ShutdownTimeout=0), Close() waits indefinitely for in-flight batches to complete.
func TestShutdownTimeout_DefaultWaitsForCompletion(t *testing.T) {
	t.Parallel()

	server, received := newSlowBatchServer(t, 200*time.Millisecond)

	// Default config - ShutdownTimeout is zero (wait indefinitely)
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 5,
		Interval:  10 * time.Millisecond,
		// ShutdownTimeout is intentionally not set (zero value = wait indefinitely)
	})
	require.NoError(t, err)

	enqueueTestCaptures(t, client, 10)

	// Close should wait for all events to be delivered despite slow server
	start := time.Now()
	err = client.Close()
	elapsed := time.Since(start)

	require.NoError(t, err, "Close() should succeed without error when waiting indefinitely")
	require.Equal(t, int64(10), received.Load(), "All events should be delivered")
	require.GreaterOrEqual(t, elapsed, 200*time.Millisecond, "Close() should have waited for slow server")
	t.Logf("Close() waited %v for slow server to complete", elapsed)
}

// TestShutdownTimeout_AbortsAfterTimeout verifies that when ShutdownTimeout is set,
// Close() aborts in-flight requests after the timeout and returns an error.
func TestShutdownTimeout_AbortsAfterTimeout(t *testing.T) {
	t.Parallel()

	serverDelay := 2 * time.Second
	server, received := newSlowBatchServer(t, serverDelay)
	var mu sync.Mutex
	var failureCount int

	callback := &testCallbackCounter{
		onFailure: func() {
			mu.Lock()
			failureCount++
			mu.Unlock()
		},
	}

	// Set a short shutdown timeout (100ms)
	client, err := NewWithConfig("test-key", Config{
		Endpoint:        server.URL,
		BatchSize:       5,
		Interval:        10 * time.Millisecond,
		ShutdownTimeout: 100 * time.Millisecond, // Short timeout - will abort
		Callback:        callback,
	})
	require.NoError(t, err)

	enqueueTestCaptures(t, client, 10)

	// Close should abort after timeout
	start := time.Now()
	err = client.Close()
	elapsed := time.Since(start)

	// Should return a timeout error
	require.Error(t, err, "Close() should return error when timeout is exceeded")
	require.Contains(t, err.Error(), "shutdown timeout", "Error should mention shutdown timeout")

	// Should have aborted relatively quickly (not waited for the slow server).
	// Allow scheduler overhead under -race on busy CI runners while still ensuring
	// shutdown returns before the server could complete normally.
	require.Less(t, elapsed, serverDelay*3/4, "Close() should abort quickly, not wait for slow server")

	// Some events may have been dropped
	mu.Lock()
	failures := failureCount
	mu.Unlock()
	t.Logf("Close() aborted after %v, received=%d, failures=%d", elapsed, received.Load(), failures)
}

// TestCloseWithContext_RespectsDeadline verifies that CloseWithContext honors
// the provided context's deadline for shutdown.
func TestCloseWithContext_RespectsDeadline(t *testing.T) {
	t.Parallel()

	serverDelay := 2 * time.Second
	server, _ := newSlowBatchServer(t, serverDelay)

	// No ShutdownTimeout configured - will use context deadline instead
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 5,
		Interval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	enqueueTestCaptures(t, client, 10)

	// Use CloseWithContext with a short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = client.CloseWithContext(ctx)
	elapsed := time.Since(start)

	// Should return a timeout error
	require.Error(t, err, "CloseWithContext should return error when context deadline exceeded")
	require.Contains(t, err.Error(), "shutdown timeout", "Error should mention shutdown timeout")

	// Should have aborted near the context deadline without waiting for the slow server.
	// Allow scheduler overhead under -race on busy CI runners while still ensuring
	// shutdown returns before the server could complete normally.
	require.Less(t, elapsed, serverDelay*3/4, "CloseWithContext should respect context deadline")
	t.Logf("CloseWithContext aborted after %v", elapsed)
}
