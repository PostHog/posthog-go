package posthog

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// BenchmarkCallback is a lightweight callback for benchmarks
// Only tracks failures to avoid overhead on the success path
type BenchmarkCallback struct {
	failureCount atomic.Int64
}

func (c *BenchmarkCallback) Success(msg APIMessage) {
	// No-op - tracking successes would add overhead to every message
}

func (c *BenchmarkCallback) Failure(msg APIMessage, err error) {
	c.failureCount.Add(1)
}

func (c *BenchmarkCallback) FailureCount() int64 {
	return c.failureCount.Load()
}

// BenchmarkConcurrentEnqueue measures throughput at different concurrency levels
// Tests: 1, 10, 100, 500, 1000 concurrent goroutines
// Uses NoOpTransport to measure pure enqueue throughput without HTTP overhead
// Note: Under extreme load, some backpressure (delivery failures) is expected
func BenchmarkConcurrentEnqueue(b *testing.B) {
	concurrencyLevels := []int{1, 10, 100, 500, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("goroutines_%d", concurrency), func(b *testing.B) {
			// Pre-generate events BEFORE benchmark
			pool := NewEventPool(b.N + 1000) // Extra events to avoid index issues

			callback := &BenchmarkCallback{}
			var enqueueErrors atomic.Int64

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				Callback:  callback,
				// Uses production defaults for BatchSize, MaxEnqueuedRequests
			})
			defer client.Close()

			b.ResetTimer()
			b.SetParallelism(concurrency)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := client.Enqueue(pool.Next()); err != nil {
						enqueueErrors.Add(1)
					}
				}
			})
			b.StopTimer()

			// Enqueue errors indicate channel full - fail immediately
			if enqueueErrors.Load() > 0 {
				b.Fatalf("Benchmark invalid: %d enqueue errors (msgs channel full)", enqueueErrors.Load())
			}
			// Delivery failures indicate batches channel backpressure - report but don't fail
			if failures := callback.FailureCount(); failures > 0 {
				b.Logf("Note: %d delivery failures (backpressure at %d goroutines)", failures, concurrency)
			}
		})
	}
}

// BenchmarkConcurrentEnqueueWithCardinality measures throughput with different property cardinalities
// Uses NoOpTransport to measure pure enqueue throughput without HTTP overhead
// Note: Under extreme load, some backpressure (delivery failures) is expected
func BenchmarkConcurrentEnqueueWithCardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_10-100_props", CardinalityLow},
		{"medium_500-2000_props", CardinalityMedium},
		{"high_2000-10000_props", CardinalityHigh},
	}
	concurrency := 100

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			// Use cardinality-appropriate pool size with pre-cloned unique maps
			pool := NewEventPoolWithDefaultSize(tc.cardinality)

			callback := &BenchmarkCallback{}
			var enqueueErrors atomic.Int64

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				Callback:  callback,
				// Uses production defaults for BatchSize, MaxEnqueuedRequests
			})
			defer client.Close()

			b.ResetTimer()
			b.SetParallelism(concurrency)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := client.Enqueue(pool.Next()); err != nil {
						enqueueErrors.Add(1)
					}
				}
			})
			b.StopTimer()

			// Enqueue errors indicate channel full - fail immediately
			if enqueueErrors.Load() > 0 {
				b.Fatalf("Benchmark invalid: %d enqueue errors (msgs channel full)", enqueueErrors.Load())
			}
			// Delivery failures indicate batches channel backpressure - report but don't fail
			if failures := callback.FailureCount(); failures > 0 {
				b.Logf("Note: %d delivery failures (backpressure at %d goroutines)", failures, concurrency)
			}
		})
	}
}

// BenchmarkEnqueueThroughput measures raw enqueue speed (no HTTP)
// Uses production defaults for BatchSize, MaxEnqueuedRequests
func BenchmarkEnqueueThroughput(b *testing.B) {
	pool := NewEventPool(b.N + 1000)

	callback := &BenchmarkCallback{}
	var enqueueErrors atomic.Int64

	// Use a transport that returns immediately (no actual HTTP)
	client, _ := NewWithConfig("test-key", Config{
		Transport: NoOpTransport(),
		Callback:  callback,
		// Uses production defaults for BatchSize, MaxEnqueuedRequests
	})
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Enqueue(pool.Next()); err != nil {
			enqueueErrors.Add(1)
		}
	}
	b.StopTimer()

	if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
		b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
			enqueueErrors.Load(), failures)
	}
}

// BenchmarkEnqueueThroughputWithCardinality measures enqueue speed at different cardinalities
// Uses production defaults for BatchSize, MaxEnqueuedRequests
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
			// Use cardinality-appropriate pool size with pre-cloned unique maps
			pool := NewEventPoolWithDefaultSize(tc.cardinality)

			callback := &BenchmarkCallback{}
			var enqueueErrors atomic.Int64

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				Callback:  callback,
				// Uses production defaults for BatchSize, MaxEnqueuedRequests
			})
			defer client.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := client.Enqueue(pool.Next()); err != nil {
					enqueueErrors.Add(1)
				}
			}
			b.StopTimer()

			if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
				b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
					enqueueErrors.Load(), failures)
			}
		})
	}
}

// BenchmarkFeatureFlagLocalEvaluation measures flag evaluation performance
func BenchmarkFeatureFlagLocalEvaluation(b *testing.B) {
	// Setup server that returns actual flag definitions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			w.Write([]byte(fixture("test-api-feature-flag.json")))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		PersonalApiKey: "test",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Pre-warm cache - wait for flags to load
	for i := 0; i < 10; i++ {
		result, _ := client.GetFeatureFlag(FeatureFlagPayload{Key: "simpleFlag", DistinctId: "warmup"})
		if result != nil {
			break
		}
	}

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
// Note: BatchSize=1 is excluded as it's pathological - creates one batch per event,
// overwhelming the worker pool with backpressure. Production min should be 10+.
func BenchmarkBatchSizes(b *testing.B) {
	batchSizes := []int{10, 50, 100, 250, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			pool := NewEventPool(b.N + 1000)

			callback := &BenchmarkCallback{}
			var enqueueErrors atomic.Int64

			client, _ := NewWithConfig("test-key", Config{
				Transport: NoOpTransport(),
				Callback:  callback,
				BatchSize: batchSize,
			})
			defer client.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := client.Enqueue(pool.Next()); err != nil {
					enqueueErrors.Add(1)
				}
			}
			b.StopTimer()

			if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
				b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
					enqueueErrors.Load(), failures)
			}
		})
	}
}

// BenchmarkEndToEndWithServer measures end-to-end throughput including HTTP
// Uses production defaults for BatchSize, MaxEnqueuedRequests
func BenchmarkEndToEndWithServer(b *testing.B) {
	pool := NewEventPool(b.N + 1000)

	server := httptest.NewServer(NoOpHandler())
	defer server.Close()

	callback := &BenchmarkCallback{}
	var enqueueErrors atomic.Int64

	client, _ := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		Callback: callback,
		// Uses production defaults for BatchSize, MaxEnqueuedRequests
	})
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Enqueue(pool.Next()); err != nil {
			enqueueErrors.Add(1)
		}
	}
	b.StopTimer()

	if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
		b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
			enqueueErrors.Load(), failures)
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
	callback := &BenchmarkCallback{}
	var enqueueErrors atomic.Int64

	// Create a client with a very fast transport
	client, _ := NewWithConfig("test-key", Config{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			io.Copy(io.Discard, r.Body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
		Callback:  callback,
		BatchSize: 10000, // Large batch to avoid flushing during benchmark
	})
	defer client.Close()

	// Pre-generate captures to avoid map race conditions
	// Each iteration needs its own Properties map since APIfy iterates over it
	captures := make([]Capture, b.N)
	for i := range captures {
		captures[i] = Capture{
			DistinctId: "user_1",
			Event:      "test_event",
			Properties: generateVariedProperties(i),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Enqueue(captures[i]); err != nil {
			enqueueErrors.Add(1)
		}
	}
	b.StopTimer()

	if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
		b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
			enqueueErrors.Load(), failures)
	}
}

// BenchmarkCompressionOverhead measures the CPU and memory overhead of GZIP compression
// Compares None vs Gzip compression modes with various payload sizes
func BenchmarkCompressionOverhead(b *testing.B) {
	compressionModes := []struct {
		name string
		mode CompressionMode
	}{
		{"none", CompressionNone},
		{"gzip", CompressionGzip},
	}

	// Test with different property cardinalities (affects payload size)
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_cardinality", CardinalityLow},
		{"medium_cardinality", CardinalityMedium},
		{"high_cardinality", CardinalityHigh},
	}

	for _, cm := range compressionModes {
		for _, card := range cardinalities {
			b.Run(fmt.Sprintf("%s/%s", cm.name, card.name), func(b *testing.B) {
				pool := NewEventPoolWithDefaultSize(card.cardinality)

				callback := &BenchmarkCallback{}
				var enqueueErrors atomic.Int64

				server := httptest.NewServer(NoOpHandler())
				defer server.Close()

				client, err := NewWithConfig("test-key", Config{
					Endpoint:    server.URL,
					Compression: cm.mode,
					Callback:    callback,
					MaxRetries:  Ptr(0),       // Disable retries to avoid noise during cleanup
					Logger:      testLogger{}, // Suppress log output in benchmarks
				})
				if err != nil {
					b.Fatalf("Failed to create client: %v", err)
				}
				defer client.Close()

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := client.Enqueue(pool.Next()); err != nil {
						enqueueErrors.Add(1)
					}
				}
				b.StopTimer()

				if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
					b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
						enqueueErrors.Load(), failures)
				}
			})
		}
	}
}

// BenchmarkEndToEndWithCompression measures end-to-end throughput with GZIP compression
// Uses production defaults for BatchSize, MaxEnqueuedRequests
func BenchmarkEndToEndWithCompression(b *testing.B) {
	compressionModes := []struct {
		name string
		mode CompressionMode
	}{
		{"none", CompressionNone},
		{"gzip", CompressionGzip},
	}

	for _, cm := range compressionModes {
		b.Run(cm.name, func(b *testing.B) {
			pool := NewEventPool(b.N + 1000)

			server := httptest.NewServer(NoOpHandler())
			defer server.Close()

			callback := &BenchmarkCallback{}
			var enqueueErrors atomic.Int64

			client, err := NewWithConfig("test-key", Config{
				Endpoint:    server.URL,
				Compression: cm.mode,
				Callback:    callback,
				MaxRetries:  Ptr(0),       // Disable retries to avoid noise during cleanup
				Logger:      testLogger{}, // Suppress log output in benchmarks
			})
			if err != nil {
				b.Fatalf("Failed to create client: %v", err)
			}
			defer client.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := client.Enqueue(pool.Next()); err != nil {
					enqueueErrors.Add(1)
				}
			}
			b.StopTimer()

			if failures := callback.FailureCount(); enqueueErrors.Load() > 0 || failures > 0 {
				b.Fatalf("Benchmark invalid: %d enqueue errors, %d delivery failures",
					enqueueErrors.Load(), failures)
			}
		})
	}
}

// BenchmarkCompressionRatio measures the compression ratio achieved
// This is not a performance benchmark but useful for understanding compression effectiveness
func BenchmarkCompressionRatio(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	for _, card := range cardinalities {
		b.Run(card.name, func(b *testing.B) {
			var uncompressedTotal, compressedTotal atomic.Int64

			// Use two servers to measure sizes
			uncompressedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				uncompressedTotal.Add(int64(len(body)))
				w.WriteHeader(200)
			}))
			defer uncompressedServer.Close()

			compressedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				compressedTotal.Add(int64(len(body)))
				w.WriteHeader(200)
			}))
			defer compressedServer.Close()

			// Send same events to both
			pool := NewEventPoolWithCardinality(100, card.cardinality)

			callbackUncompressed := &BenchmarkCallback{}
			callbackCompressed := &BenchmarkCallback{}
			var enqueueErrorsUncompressed, enqueueErrorsCompressed atomic.Int64

			clientUncompressed, err := NewWithConfig("test-key", Config{
				Endpoint:    uncompressedServer.URL,
				Compression: CompressionNone,
				BatchSize:   50,
				Callback:    callbackUncompressed,
				MaxRetries:  Ptr(0),       // Disable retries to avoid noise during cleanup
				Logger:      testLogger{}, // Suppress log output in benchmarks
			})
			if err != nil {
				b.Fatalf("Failed to create uncompressed client: %v", err)
			}

			clientCompressed, err := NewWithConfig("test-key", Config{
				Endpoint:    compressedServer.URL,
				Compression: CompressionGzip,
				BatchSize:   50,
				Callback:    callbackCompressed,
				MaxRetries:  Ptr(0),       // Disable retries to avoid noise during cleanup
				Logger:      testLogger{}, // Suppress log output in benchmarks
			})
			if err != nil {
				b.Fatalf("Failed to create compressed client: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N && i < 100; i++ {
				event := pool.Next()
				if err := clientUncompressed.Enqueue(event); err != nil {
					enqueueErrorsUncompressed.Add(1)
				}
				// Create a copy for the compressed client to avoid race
				eventCopy := Capture{
					DistinctId: event.DistinctId,
					Event:      event.Event,
					Properties: cloneProperties(event.Properties),
				}
				if err := clientCompressed.Enqueue(eventCopy); err != nil {
					enqueueErrorsCompressed.Add(1)
				}
			}
			b.StopTimer()

			clientUncompressed.Close()
			clientCompressed.Close()

			// Verify no errors occurred
			if enqueueErrorsUncompressed.Load() > 0 || callbackUncompressed.FailureCount() > 0 {
				b.Fatalf("Uncompressed client: %d enqueue errors, %d delivery failures",
					enqueueErrorsUncompressed.Load(), callbackUncompressed.FailureCount())
			}
			if enqueueErrorsCompressed.Load() > 0 || callbackCompressed.FailureCount() > 0 {
				b.Fatalf("Compressed client: %d enqueue errors, %d delivery failures",
					enqueueErrorsCompressed.Load(), callbackCompressed.FailureCount())
			}

			uncompBytes := uncompressedTotal.Load()
			compBytes := compressedTotal.Load()
			if uncompBytes == 0 || compBytes == 0 {
				b.Fatalf("No data received: uncompressed=%d, compressed=%d", uncompBytes, compBytes)
			}

			ratio := float64(compBytes) / float64(uncompBytes) * 100
			b.Logf("Cardinality %s: Uncompressed=%d bytes, Compressed=%d bytes, Ratio=%.1f%%",
				card.name, uncompBytes, compBytes, ratio)
		})
	}
}
