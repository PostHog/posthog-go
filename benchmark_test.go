package posthog

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkConcurrentEnqueue measures throughput at different concurrency levels
// Tests: 1, 10, 100, 500, 1000 concurrent goroutines
func BenchmarkConcurrentEnqueue(b *testing.B) {
	concurrencyLevels := []int{1, 10, 100, 500, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("goroutines_%d", concurrency), func(b *testing.B) {
			// Pre-generate events BEFORE benchmark
			pool := NewEventPool(b.N + 1000) // Extra events to avoid index issues

			server := httptest.NewServer(NoOpHandler())
			defer server.Close()

			client, _ := NewWithConfig("test-key", Config{
				Endpoint:   server.URL,
				BatchSize:  100,
				NumWorkers: 4,
			})
			defer client.Close()

			b.ResetTimer()
			b.SetParallelism(concurrency)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					client.Enqueue(pool.Next())
				}
			})
		})
	}
}

// BenchmarkConcurrentEnqueueWithCardinality measures throughput with different property cardinalities
func BenchmarkConcurrentEnqueueWithCardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_10-100_props", CardinalityLow},
		{"medium_500-2000_props", CardinalityMedium},
	}
	concurrency := 100

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			pool := NewEventPoolWithCardinality(b.N+1000, tc.cardinality)

			server := httptest.NewServer(NoOpHandler())
			defer server.Close()

			client, _ := NewWithConfig("test-key", Config{
				Endpoint:   server.URL,
				BatchSize:  50,
				NumWorkers: 4,
			})
			defer client.Close()

			b.ResetTimer()
			b.SetParallelism(concurrency)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					client.Enqueue(pool.Next())
				}
			})
		})
	}
}

// BenchmarkEnqueueThroughput measures raw enqueue speed (no HTTP)
func BenchmarkEnqueueThroughput(b *testing.B) {
	pool := NewEventPool(b.N + 1000)

	// Use a transport that returns immediately (no actual HTTP)
	client, _ := NewWithConfig("test-key", Config{
		Transport: NoOpTransport(),
		BatchSize: 250,
	})
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.Enqueue(pool.Next())
	}
}

// BenchmarkEnqueueThroughputWithCardinality measures enqueue speed at different cardinalities
func BenchmarkEnqueueThroughputWithCardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			pool := NewEventPoolWithCardinality(b.N+1000, tc.cardinality)

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				BatchSize: 250,
			})
			defer client.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				client.Enqueue(pool.Next())
			}
		})
	}
}

// BenchmarkFeatureFlagLocalEvaluation measures flag evaluation performance
func BenchmarkFeatureFlagLocalEvaluation(b *testing.B) {
	// Setup client with pre-loaded flags (no network calls)
	server := httptest.NewServer(NoOpHandler())
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		PersonalApiKey: "test",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Pre-warm cache
	client.GetFeatureFlag(FeatureFlagPayload{Key: "simpleFlag", DistinctId: "warmup"})

	distinctIds := make([]string, 1000)
	for i := range distinctIds {
		distinctIds[i] = fmt.Sprintf("user_%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "simpleFlag",
			DistinctId: distinctIds[i%len(distinctIds)],
		})
	}
}

// BenchmarkBatchSizes measures throughput at various batch sizes
func BenchmarkBatchSizes(b *testing.B) {
	batchSizes := []int{1, 10, 50, 100, 250, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			pool := NewEventPool(b.N + 1000)

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				BatchSize: batchSize,
			})
			defer client.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				client.Enqueue(pool.Next())
			}
		})
	}
}

// BenchmarkEndToEndWithServer measures end-to-end throughput including HTTP
func BenchmarkEndToEndWithServer(b *testing.B) {
	pool := NewEventPool(b.N + 1000)

	server := httptest.NewServer(NoOpHandler())
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		Endpoint:   server.URL,
		BatchSize:  100,
		NumWorkers: 4,
	})
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.Enqueue(pool.Next())
	}
}

// NoOpHandler returns an HTTP handler that discards the body and returns 200
func NoOpHandler() noOpHandler {
	return noOpHandler{}
}

type noOpHandler struct{}

func (h noOpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	w.WriteHeader(200)
}

// BenchmarkEventPoolGeneration measures event pool creation time
func BenchmarkEventPoolGeneration(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				NewEventPool(size)
			}
		})
	}
}

// BenchmarkEventPoolGenerationWithCardinality measures pool creation with cardinality
func BenchmarkEventPoolGenerationWithCardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	size := 100 // Keep small for high cardinality

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				NewEventPoolWithCardinality(size, tc.cardinality)
			}
		})
	}
}

// BenchmarkValidation measures message validation overhead
func BenchmarkValidation(b *testing.B) {
	capture := Capture{
		DistinctId: "user_1",
		Event:      "test_event",
		Properties: generateVariedProperties(42),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		capture.Validate()
	}
}

// BenchmarkAPIfy measures APIfy conversion overhead
func BenchmarkAPIfy(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			capture := Capture{
				DistinctId: "user_1",
				Event:      "test_event",
				Properties: generatePropertiesWithCardinality(42, tc.cardinality),
				Groups:     generateGroupsWithCardinality(42, tc.cardinality),
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				capture.APIfy()
			}
		})
	}
}

// BenchmarkMessageEnqueueOverhead measures the overhead of the Enqueue path
func BenchmarkMessageEnqueueOverhead(b *testing.B) {
	// Create a client with a very fast transport
	client, _ := NewWithConfig("test-key", Config{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			io.Copy(io.Discard, r.Body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
		BatchSize: 10000, // Large batch to avoid flushing during benchmark
	})
	defer client.Close()

	capture := Capture{
		DistinctId: "user_1",
		Event:      "test_event",
		Properties: generateVariedProperties(42),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.Enqueue(capture)
	}
}

