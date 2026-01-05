package posthog

import (
	"fmt"

	json "github.com/goccy/go-json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStress_EndToEndThroughput measures end-to-end delivery at various loads
func TestStress_EndToEndThroughput(t *testing.T) {
	eventCounts := []int{100, 250, 500}
	concurrencyLevels := []int{1, 10, 100, 500, 1000}

	for _, eventCount := range eventCounts {
		for _, concurrency := range concurrencyLevels {
			t.Run(fmt.Sprintf("events_%d_goroutines_%d", eventCount, concurrency), func(t *testing.T) {
				// PRE-GENERATE all events with realistic cardinality distribution
				pool := NewEventPoolWithCardinalityDistribution(eventCount)
				events := pool.Events()

				var received atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var b batch
					json.NewDecoder(r.Body).Decode(&b)
					received.Add(int64(len(b.Messages)))
					w.WriteHeader(200)
				}))
				defer server.Close()

				client, err := NewWithConfig("test-key", Config{
					Endpoint: server.URL,
					// Uses production defaults for BatchSize, MaxEnqueuedRequests
				})
				require.NoError(t, err)

				start := time.Now()

				// Distribute events across goroutines
				var wg sync.WaitGroup
				var errorCount atomic.Int64
				eventsPerGoroutine := eventCount / concurrency
				if eventsPerGoroutine == 0 {
					eventsPerGoroutine = 1
				}

				actualConcurrency := concurrency
				if actualConcurrency > eventCount {
					actualConcurrency = eventCount
				}

				for g := 0; g < actualConcurrency && g*eventsPerGoroutine < eventCount; g++ {
					wg.Add(1)
					startIdx := g * eventsPerGoroutine
					endIdx := (g + 1) * eventsPerGoroutine
					if endIdx > eventCount {
						endIdx = eventCount
					}
					// Handle remainder for last goroutine
					if g == actualConcurrency-1 {
						endIdx = eventCount
					}

					go func(start, end int) {
						defer wg.Done()
						for i := start; i < end; i++ {
							if err := client.Enqueue(events[i]); err != nil {
								errorCount.Add(1)
							}
						}
					}(startIdx, endIdx)
				}

				wg.Wait()
				client.Close()

				elapsed := time.Since(start)
				throughput := float64(eventCount) / elapsed.Seconds()

				t.Logf("Throughput: %.2f events/sec, Total time: %v", throughput, elapsed)

				require.Equal(t, int64(0), errorCount.Load(),
					"No enqueue errors should occur")
				require.Equal(t, int64(eventCount), received.Load(),
					"All events should be delivered")
			})
		}
	}
}

// TestStress_BatchSizeBoundaries tests behavior at batch size limits
func TestStress_BatchSizeBoundaries(t *testing.T) {
	t.Parallel()
	// Test batch sizes around common boundaries (50, 250 are defaults)
	batchSizes := []int{49, 50, 51, 100, 249, 250, 251, 500}

	for _, batchSize := range batchSizes {
		t.Run(fmt.Sprintf("batch_%d", batchSize), func(t *testing.T) {
			// Use enough events to force multiple batches (at least 3x batch size, minimum 300)
			eventCount := batchSize * 3
			if eventCount < 300 {
				eventCount = 300
			}
			pool := NewEventPoolWithCardinalityDistribution(eventCount)

			var batchesReceived atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				batchesReceived.Add(1)
				w.WriteHeader(200)
			}))
			defer server.Close()

			client, err := NewWithConfig("test-key", Config{
				Endpoint:  server.URL,
				BatchSize: batchSize,
			})
			require.NoError(t, err)

			var errorCount int
			for _, e := range pool.Events() {
				if err := client.Enqueue(e); err != nil {
					errorCount++
				}
			}
			client.Close()
			require.Equal(t, 0, errorCount, "No enqueue errors should occur")

			expectedBatches := (eventCount + batchSize - 1) / batchSize
			// Allow for some variance due to byte-based batching
			minExpected := expectedBatches / 2
			if minExpected < 2 {
				minExpected = 2
			}
			if batchesReceived.Load() < int64(minExpected) {
				t.Errorf("Expected at least %d batches (ideal: %d), got %d",
					minExpected, expectedBatches, batchesReceived.Load())
			}
		})
	}
}

// TestStress_CardinalityDistribution verifies system handles various property cardinalities
func TestStress_CardinalityDistribution(t *testing.T) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_10-100_props", CardinalityLow},
		{"medium_500-2000_props", CardinalityMedium},
		{"high_2000-10000_props", CardinalityHigh},
	}

	eventCount := 200
	concurrency := 50

	for _, tc := range cardinalities {
		t.Run(tc.name, func(t *testing.T) {
			// Pre-generate events with specific cardinality
			events := make([]Capture, eventCount)
			for i := range events {
				events[i] = Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      realisticEvents[i%len(realisticEvents)],
					Timestamp:  time.Now(),
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}
			}

			var received atomic.Int64
			var totalBytes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				totalBytes.Add(int64(len(body)))
				var b batch
				json.Unmarshal(body, &b)
				received.Add(int64(len(b.Messages)))
				w.WriteHeader(200)
			}))
			defer server.Close()

		client, err := NewWithConfig("test-key", Config{
			Endpoint: server.URL,
			// Uses production defaults for BatchSize, MaxEnqueuedRequests
		})
		require.NoError(t, err)

			start := time.Now()

			var wg sync.WaitGroup
			var errorCount atomic.Int64
			eventsPerGoroutine := eventCount / concurrency
			for g := 0; g < concurrency; g++ {
				wg.Add(1)
				startIdx := g * eventsPerGoroutine
				endIdx := (g + 1) * eventsPerGoroutine
				if g == concurrency-1 {
					endIdx = eventCount
				}
				go func(start, end int) {
					defer wg.Done()
					for i := start; i < end; i++ {
						if err := client.Enqueue(events[i]); err != nil {
							errorCount.Add(1)
						}
					}
				}(startIdx, endIdx)
			}

			wg.Wait()
			client.Close()

			elapsed := time.Since(start)
			throughput := float64(eventCount) / elapsed.Seconds()
			avgBytesPerEvent := float64(totalBytes.Load()) / float64(received.Load())

			t.Logf("Cardinality: %s, Throughput: %.2f events/sec, Avg bytes/event: %.0f",
				tc.name, throughput, avgBytesPerEvent)

			require.Equal(t, int64(0), errorCount.Load(),
				"No enqueue errors should occur")
			require.Equal(t, int64(eventCount), received.Load(),
				"All events should be delivered")
		})
	}
}

// TestStress_HighConcurrencyLowVolume tests many goroutines with few events each
func TestStress_HighConcurrencyLowVolume(t *testing.T) {
	goroutines := 1000
	eventsPerGoroutine := 5
	totalEvents := goroutines * eventsPerGoroutine

	pool := NewEventPool(totalEvents)

	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		// Uses production defaults for BatchSize, MaxEnqueuedRequests
	})
	require.NoError(t, err)

	start := time.Now()

	var wg sync.WaitGroup
	var errorCount atomic.Int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				idx := goroutineID*eventsPerGoroutine + i
				if err := client.Enqueue(pool.Get(idx)); err != nil {
					errorCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	elapsed := time.Since(start)
	throughput := float64(totalEvents) / elapsed.Seconds()

	t.Logf("High concurrency: %d goroutines, %d events, %.2f events/sec, %v total",
		goroutines, totalEvents, throughput, elapsed)
	require.Equal(t, int64(0), errorCount.Load(),
		"No enqueue errors should occur")

	require.Equal(t, int64(totalEvents), received.Load(),
		"All events should be delivered")
}

// TestStress_LowConcurrencyHighVolume tests few goroutines with many events each
func TestStress_LowConcurrencyHighVolume(t *testing.T) {
	goroutines := 4
	eventsPerGoroutine := 125
	totalEvents := goroutines * eventsPerGoroutine

	pool := NewEventPool(totalEvents)

	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		// Uses production defaults for BatchSize, MaxEnqueuedRequests
	})
	require.NoError(t, err)

	start := time.Now()

	var wg sync.WaitGroup
	var errorCount atomic.Int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				idx := goroutineID*eventsPerGoroutine + i
				if err := client.Enqueue(pool.Get(idx)); err != nil {
					errorCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	elapsed := time.Since(start)
	throughput := float64(totalEvents) / elapsed.Seconds()

	t.Logf("Low concurrency: %d goroutines, %d events, %.2f events/sec, %v total",
		goroutines, totalEvents, throughput, elapsed)
	require.Equal(t, int64(0), errorCount.Load(),
		"No enqueue errors should occur")

	require.Equal(t, int64(totalEvents), received.Load(),
		"All events should be delivered")
}

// TestStress_MixedCardinality tests a mix of cardinalities in a single test
func TestStress_MixedCardinality(t *testing.T) {
	eventCount := 300
	concurrency := 30

	// Generate events using the realistic distribution
	pool := NewEventPoolWithCardinalityDistribution(eventCount)

	var received atomic.Int64
	var totalBytes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		totalBytes.Add(int64(len(body)))
		var b batch
		json.Unmarshal(body, &b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		// Uses production defaults for BatchSize, MaxEnqueuedRequests
	})
	require.NoError(t, err)

	start := time.Now()

	var wg sync.WaitGroup
	var errorCount atomic.Int64
	eventsPerGoroutine := eventCount / concurrency
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		startIdx := g * eventsPerGoroutine
		endIdx := (g + 1) * eventsPerGoroutine
		if g == concurrency-1 {
			endIdx = eventCount
		}
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				if err := client.Enqueue(pool.Get(i)); err != nil {
					errorCount.Add(1)
				}
			}
		}(startIdx, endIdx)
	}

	wg.Wait()
	client.Close()

	elapsed := time.Since(start)
	throughput := float64(eventCount) / elapsed.Seconds()
	avgBytesPerEvent := float64(0)
	if received.Load() > 0 {
		avgBytesPerEvent = float64(totalBytes.Load()) / float64(received.Load())
	}

	t.Logf("Mixed cardinality: %d events, %.2f events/sec, %.0f avg bytes/event",
		eventCount, throughput, avgBytesPerEvent)

	require.Equal(t, int64(0), errorCount.Load(),
		"No enqueue errors should occur")
	require.Equal(t, int64(eventCount), received.Load(),
		"All events should be delivered")
}

// TestStress_RapidCloseReopen tests rapid client close and reopen
func TestStress_RapidCloseReopen(t *testing.T) {
	t.Parallel()
	iterations := 10
	eventsPerIteration := 50

	pool := NewEventPool(eventsPerIteration * iterations)

	var totalReceived atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		totalReceived.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	var totalErrors int
	for i := 0; i < iterations; i++ {
		client, err := NewWithConfig("test-key", Config{
			Endpoint:  server.URL,
			BatchSize: 25,
			// Uses production defaults for MaxEnqueuedRequests
		})
		require.NoError(t, err)

		for j := 0; j < eventsPerIteration; j++ {
			idx := i*eventsPerIteration + j
			if err := client.Enqueue(pool.Get(idx)); err != nil {
				totalErrors++
			}
		}

		client.Close()
	}

	require.Equal(t, 0, totalErrors,
		"No enqueue errors should occur")
	expectedTotal := int64(iterations * eventsPerIteration)
	require.Equal(t, expectedTotal, totalReceived.Load(),
		"All events across all iterations should be delivered")
}

// TestStress_PrepareForSendUnderLoad verifies prepareForSend works correctly under concurrent load
func TestStress_PrepareForSendUnderLoad(t *testing.T) {
	t.Parallel()
	cardinalities := []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh}

	for _, cardinality := range cardinalities {
		t.Run(CardinalityName(cardinality), func(t *testing.T) {
			pool := NewEventPoolWithCardinality(100, cardinality)

			var wg sync.WaitGroup
			errorCount := atomic.Int64{}

			// Concurrent prepareForSend
			for g := 0; g < 10; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 100; i++ {
						capture := pool.Get(i)
						capture.Type = "capture"
						data, apiMsg, err := prepareForSend(capture)
						if err != nil {
							errorCount.Add(1)
						}
						if len(data) <= 0 {
							errorCount.Add(1)
						}
						if apiMsg == nil {
							errorCount.Add(1)
						}
					}
				}()
			}

			wg.Wait()
			require.Equal(t, int64(0), errorCount.Load(),
				"prepareForSend should never return error, non-positive size, or nil message")
		})
	}
}

