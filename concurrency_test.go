package posthog

import (
	"fmt"

	json "github.com/goccy/go-json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConcurrentEnqueue tests thread-safety of the client under concurrent load
func TestConcurrentEnqueue(t *testing.T) {
	t.Parallel()
	goroutines := 20
	eventsPerGoroutine := 50
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

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				idx := goroutineID*eventsPerGoroutine + i
				err := client.Enqueue(pool.Get(idx))
				if err != nil {
					t.Errorf("Enqueue failed: %v", err)
				}
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	require.Equal(t, int64(totalEvents), received.Load(),
		"All events should be delivered")
}

// TestConcurrentEnqueueDifferentMessageTypes tests concurrent enqueueing of different message types
func TestConcurrentEnqueueDifferentMessageTypes(t *testing.T) {
	t.Parallel()
	goroutines := 10
	messagesPerGoroutine := 20

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

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < messagesPerGoroutine; i++ {
				msgType := i % 4
				switch msgType {
				case 0:
					client.Enqueue(Capture{
						DistinctId: fmt.Sprintf("user_%d", goroutineID),
						Event:      fmt.Sprintf("event_%d", i),
					})
				case 1:
					client.Enqueue(Identify{
						DistinctId: fmt.Sprintf("user_%d", goroutineID),
						Properties: Properties{"name": fmt.Sprintf("User %d", goroutineID)},
					})
				case 2:
					client.Enqueue(Alias{
						DistinctId: fmt.Sprintf("user_%d", goroutineID),
						Alias:      fmt.Sprintf("alias_%d", goroutineID),
					})
				case 3:
					client.Enqueue(GroupIdentify{
						Type: "company",
						Key:  fmt.Sprintf("company_%d", goroutineID),
						Properties: Properties{
							"name": fmt.Sprintf("Company %d", goroutineID),
						},
					})
				}
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	expectedTotal := int64(goroutines * messagesPerGoroutine)
	require.Equal(t, expectedTotal, received.Load(),
		"All messages should be delivered")
}

// TestConcurrentFeatureFlagEvaluation tests concurrent flag evaluation with cache updates
func TestConcurrentFeatureFlagEvaluation(t *testing.T) {
	t.Parallel()
	// Setup mock server with feature flags
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// Return proper format for both local evaluation and flags endpoints
		if r.URL.Path == "/api/feature_flag/local_evaluation" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"flags": []map[string]interface{}{
					{
						"id":     1,
						"key":    "test-flag",
						"active": true,
						"filters": map[string]interface{}{
							"groups": []map[string]interface{}{
								{
									"rollout_percentage": 100,
								},
							},
						},
					},
				},
				"group_type_mapping": map[string]string{},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"featureFlags": map[string]interface{}{
					"test-flag": true,
				},
			})
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:       server.URL,
		PersonalApiKey: "test-key",
	})
	require.NoError(t, err)
	defer client.Close()

	// Wait for initial flag fetch to complete
	time.Sleep(100 * time.Millisecond)

	goroutines := 100
	evaluationsPerGoroutine := 50

	var wg sync.WaitGroup
	errorCount := atomic.Int64{}

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < evaluationsPerGoroutine; i++ {
				distinctId := fmt.Sprintf("user_%d_%d", goroutineID, i)
				_, err := client.GetFeatureFlag(FeatureFlagPayload{
					Key:        "test-flag",
					DistinctId: distinctId,
				})
				if err != nil {
					errorCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	// With httptest.Server, there are no network issues - all evaluations should succeed
	require.Equal(t, int64(0), errorCount.Load(),
		"All feature flag evaluations should succeed - httptest.Server has no network issues")
}

// TestConcurrentClientOperations tests mixing Enqueue with Close operations
func TestConcurrentClientOperations(t *testing.T) {
	t.Parallel()
	// This tests that Close() properly waits for all pending operations

	for iteration := 0; iteration < 5; iteration++ {
		t.Run(fmt.Sprintf("iteration_%d", iteration), func(t *testing.T) {
			var received atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b batch
				json.NewDecoder(r.Body).Decode(&b)
				received.Add(int64(len(b.Messages)))
				w.WriteHeader(200)
			}))
			defer server.Close()

			client, err := NewWithConfig("test-key", Config{
				Endpoint:   server.URL,
				BatchSize:  50,
				MaxEnqueuedRequests: 100,
			})
			require.NoError(t, err)

			// Start enqueueing
			enqueueCount := 50
			var wg sync.WaitGroup

			for i := 0; i < enqueueCount; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					client.Enqueue(Capture{
						DistinctId: fmt.Sprintf("user_%d", idx),
						Event:      "test_event",
					})
				}(i)
			}

			// Wait for all enqueues to complete
			wg.Wait()

			// Close should wait for all events to be sent
			err = client.Close()
			require.NoError(t, err)

			// All events should have been delivered by the time Close returns
			require.Equal(t, int64(enqueueCount), received.Load(),
				"All events should be delivered before Close returns")
		})
	}
}

// TestConcurrentEnqueueWithSlowServer tests behavior with a slow server
func TestConcurrentEnqueueWithSlowServer(t *testing.T) {
	goroutines := 20
	eventsPerGoroutine := 10
	totalEvents := goroutines * eventsPerGoroutine

	pool := NewEventPool(totalEvents)

	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow server
		time.Sleep(10 * time.Millisecond)
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

	var wg sync.WaitGroup
	start := time.Now()

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				idx := goroutineID*eventsPerGoroutine + i
				client.Enqueue(pool.Get(idx))
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	elapsed := time.Since(start)
	t.Logf("Slow server test: %d events in %v", totalEvents, elapsed)

	require.Equal(t, int64(totalEvents), received.Load(),
		"All events should be delivered even with slow server")
}

// TestConcurrentPrepareForSend tests that prepareForSend is thread-safe
func TestConcurrentPrepareForSend(t *testing.T) {
	t.Parallel()
	capture := Capture{
		Type:       "capture",
		DistinctId: "test_user",
		Event:      "test_event",
		Properties: generatePropertiesWithCardinality(42, CardinalityMedium),
		Groups:     generateGroupsWithCardinality(42, CardinalityMedium),
	}

	goroutines := 100
	callsPerGoroutine := 100

	var wg sync.WaitGroup
	results := make([]int, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			var total int
			for i := 0; i < callsPerGoroutine; i++ {
				data, _, err := prepareForSend(capture)
				if err != nil {
					t.Errorf("prepareForSend error: %v", err)
					return
				}
				total += len(data)
			}
			results[goroutineID] = total / callsPerGoroutine
		}(g)
	}

	wg.Wait()

	// All goroutines should get the same average result
	first := results[0]
	for i, result := range results {
		if result != first {
			t.Errorf("Inconsistent prepareForSend size: goroutine 0 got %d, goroutine %d got %d",
				first, i, result)
		}
	}
}

// TestConcurrentEventPoolAccess tests thread-safe access to EventPool
func TestConcurrentEventPoolAccess(t *testing.T) {
	t.Parallel()
	poolSize := 1000
	pool := NewEventPool(poolSize)

	goroutines := 50
	accessesPerGoroutine := 200

	var wg sync.WaitGroup
	errorCount := atomic.Int64{}

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < accessesPerGoroutine; i++ {
				event := pool.Next()
				if event.DistinctId == "" || event.Event == "" {
					errorCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	require.Equal(t, int64(0), errorCount.Load(),
		"No errors should occur during concurrent pool access")
}

// TestConcurrentCallbackExecution tests that callbacks are executed thread-safely
func TestConcurrentCallbackExecution(t *testing.T) {
	t.Parallel()
	callback := NewUnifiedCallback(t)

	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b batch
		json.NewDecoder(r.Body).Decode(&b)
		received.Add(int64(len(b.Messages)))
		w.WriteHeader(200)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-key", Config{
		Endpoint:   server.URL,
		BatchSize:  50,
		MaxEnqueuedRequests: 100,
		Callback:   callback,
	})
	require.NoError(t, err)

	goroutines := 10
	eventsPerGoroutine := 20

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				client.Enqueue(Capture{
					DistinctId: fmt.Sprintf("user_%d", goroutineID),
					Event:      fmt.Sprintf("event_%d", i),
				})
			}
		}(g)
	}

	wg.Wait()
	client.Close()

	successCount, failureCount := callback.GetCounts()
	t.Logf("Callback counts: success=%d, failure=%d", successCount, failureCount)

	// All events should have been delivered with zero failures
	totalEvents := int64(goroutines * eventsPerGoroutine)
	require.Equal(t, totalEvents, received.Load(),
		"All events should be delivered")
	require.Equal(t, 0, failureCount,
		"No callback failures should occur")
}

// TestRaceConditionDetection is specifically designed to trigger race detection
// Run with: go test -race -run TestRaceConditionDetection
func TestRaceConditionDetection(t *testing.T) {
	t.Parallel()
	t.Run("concurrent_enqueue_and_close", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
		defer server.Close()

		for i := 0; i < 10; i++ {
			client, _ := NewWithConfig("test-key", Config{
				Endpoint: server.URL,
				// Uses production defaults for BatchSize, MaxEnqueuedRequests
			})

			var wg sync.WaitGroup

			// Concurrent enqueues
			for j := 0; j < 20; j++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					client.Enqueue(Capture{
						DistinctId: fmt.Sprintf("user_%d", idx),
						Event:      "test",
					})
				}(j)
			}

			wg.Wait()
			client.Close()
		}
	})

	t.Run("concurrent_pool_access", func(t *testing.T) {
		pool := NewEventPool(100)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					_ = pool.Next()
				}
			}()
		}
		wg.Wait()
	})
}

