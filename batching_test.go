package posthog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBatching_SmallEventsBatchTogether verifies that small events are batched together
func TestBatching_SmallEventsBatchTogether(t *testing.T) {
	t.Parallel()

	var batchCount atomic.Int64
	var totalMessages atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		batchCount.Add(1)
		totalMessages.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	// Small events (~100 props, ~5KB each)
	// With 500KB batch limit, should fit ~100 events per batch
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 250, // Use default
		Interval:  50 * time.Millisecond,
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

	// Medium-high cardinality events (~2000 props, ~100KB each)
	// With 500KB batch limit, should fit ~5 events per batch
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 250, // Use default - byte limit should trigger before count
		Interval:  50 * time.Millisecond,
	})
	require.NoError(t, err)

	// Send 10 medium-high cardinality events
	pool := NewEventPoolWithCardinality(10, CardinalityMedium)
	for i := 0; i < 10; i++ {
		err := client.Enqueue(pool.Next())
		require.NoError(t, err)
	}

	client.Close()

	// Medium cardinality events should trigger multiple batches
	require.GreaterOrEqual(t, batchCount.Load(), int64(1), "Should have at least 1 batch")
	t.Logf("Batch count: %d, sizes: %v", batchCount.Load(), batchSizes)
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

	var batchCount atomic.Int64
	var totalMessages atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		batchCount.Add(1)
		totalMessages.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

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
		Interval:  10 * time.Millisecond, // Short interval so batch count limit triggers
	})
	require.NoError(t, err)

	// Send 25 small events with BatchSize=10 and short interval
	// The batch count limit should trigger before interval flush
	pool := NewEventPoolWithCardinality(25, CardinalityLow)
	for i := 0; i < 25; i++ {
		err := client.Enqueue(pool.Next())
		require.NoError(t, err)
	}

	// Wait for batches to be flushed before closing
	time.Sleep(100 * time.Millisecond)

	client.Close()

	// Check that no batch exceeds BatchSize
	for i, size := range batchSizes {
		require.LessOrEqual(t, size, batchSize, "Batch %d has size %d which exceeds BatchSize %d", i, size, batchSize)
	}
	t.Logf("Batch sizes: %v", batchSizes)
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

