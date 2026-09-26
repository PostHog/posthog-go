package posthog

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type BenchmarkCallback struct {
	successCount atomic.Int64
	failureCount atomic.Int64
}

func (c *BenchmarkCallback) Success(APIMessage)        { c.successCount.Add(1) }
func (c *BenchmarkCallback) Failure(APIMessage, error) { c.failureCount.Add(1) }
func (c *BenchmarkCallback) FailureCount() int64       { return c.failureCount.Load() }

// Producer benchmarks exclude draining from timing, but account for every outcome
// after draining. A rejected enqueue is overload, not completed delivery throughput.
func benchmarkEnqueue(b *testing.B, pool *EventPool, parallelism int) {
	callback := &BenchmarkCallback{}
	client, err := NewWithConfig("test-key", Config{
		Transport: NoOpTransport(), Callback: callback, Logger: testLogger{},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	var drops, unexpected atomic.Int64
	enqueue := func() {
		if err := client.Enqueue(pool.Next()); err != nil {
			if errors.Is(err, ErrQueueFull) {
				drops.Add(1)
			} else {
				unexpected.Add(1)
			}
		}
	}
	b.ResetTimer()
	if parallelism > 0 {
		b.SetParallelism(parallelism)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				enqueue()
			}
		})
	} else {
		for i := 0; i < b.N; i++ {
			enqueue()
		}
	}
	b.StopTimer()
	if err := client.Close(); err != nil {
		b.Fatal(err)
	}
	if unexpected.Load() != 0 {
		b.Fatalf("unexpected enqueue errors: %d", unexpected.Load())
	}
	if got := drops.Load() + callback.successCount.Load() + callback.FailureCount(); got != int64(b.N) {
		b.Fatalf("accounted for %d events, want %d", got, b.N)
	}
	b.ReportMetric(100*float64(drops.Load())/float64(b.N), "drop%")
	b.ReportMetric(100*float64(callback.FailureCount())/float64(b.N), "delivery-failure%")
}

func BenchmarkConcurrentEnqueue(b *testing.B) {
	for _, parallelism := range []int{1, 10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("parallelism_%d", parallelism), func(b *testing.B) {
			benchmarkEnqueue(b, NewEventPool(1000), parallelism)
		})
	}
}

func BenchmarkConcurrentEnqueueWithCardinality(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		b.Run(CardinalityName(card), func(b *testing.B) {
			benchmarkEnqueue(b, NewEventPoolWithDefaultSize(card), 100)
		})
	}
}

func BenchmarkEnqueueThroughput(b *testing.B) { benchmarkEnqueue(b, NewEventPool(1000), 0) }

func BenchmarkEnqueueThroughputWithCardinality(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		b.Run(CardinalityName(card), func(b *testing.B) {
			benchmarkEnqueue(b, NewEventPoolWithDefaultSize(card), 0)
		})
	}
}

func BenchmarkFeatureFlagLocalEvaluation(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			_, _ = w.Write([]byte(fixture("test-api-feature-flag.json")))
		} else {
			http.Error(w, "unexpected remote evaluation or capture", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client, err := NewWithConfig("test-key", Config{PersonalApiKey: "test", Endpoint: server.URL})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	payload := FeatureFlagPayload{Key: "simpleFlag", DistinctId: "warmup", OnlyEvaluateLocally: true, SendFeatureFlagEvents: Ptr(false)}
	if value, err := client.GetFeatureFlag(payload); err != nil || value != true {
		b.Fatalf("local flag warmup: value=%v err=%v", value, err)
	}
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = fmt.Sprintf("user_%d", i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload.DistinctId = ids[i%len(ids)]
		if _, err := client.GetFeatureFlag(payload); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
}

// Flush each bounded producer window to measure completed work without turning
// queue overflow into an apparent throughput improvement. Drain is timed.
func benchmarkDelivery(b *testing.B, cfg Config, pool *EventPool) {
	callback := &BenchmarkCallback{}
	cfg.Callback, cfg.Logger = callback, testLogger{}
	client, err := NewWithConfig("test-key", cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	window := cfg.BatchSize
	if window == 0 {
		window = DefaultBatchSize
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Enqueue(pool.Next()); err != nil {
			b.Fatal(err)
		}
		if (i+1)%window == 0 {
			if err := client.Flush(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := client.Close(); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if got := callback.successCount.Load(); got != int64(b.N) || callback.FailureCount() != 0 {
		b.Fatalf("delivered=%d failures=%d, want %d/0", got, callback.FailureCount(), b.N)
	}
}

func BenchmarkBatchSizes(b *testing.B) {
	for _, size := range []int{10, 50, 100, 250, 500} {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			benchmarkDelivery(b, Config{Transport: NoOpTransport(), BatchSize: size}, NewEventPool(1000))
		})
	}
}

func BenchmarkEndToEndWithServer(b *testing.B) {
	server := httptest.NewServer(NoOpHandler())
	defer server.Close()
	benchmarkDelivery(b, Config{Endpoint: server.URL}, NewEventPool(1000))
}

func NoOpHandler() noOpHandler { return noOpHandler{} }

type noOpHandler struct{}

func (noOpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
}

var benchmarkPoolSink *EventPool
var benchmarkMessageSink APIMessage

func BenchmarkEventPoolGeneration(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				benchmarkPoolSink = NewEventPool(size)
			}
		})
	}
}

func BenchmarkEventPoolGenerationWithCardinality(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		b.Run(CardinalityName(card), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				benchmarkPoolSink = NewEventPoolWithCardinality(100, card)
			}
		})
	}
}

func BenchmarkValidation(b *testing.B) {
	capture := Capture{DistinctId: "user_1", Event: "test_event", Properties: generateVariedProperties(42)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := capture.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAPIfy(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		b.Run(CardinalityName(card), func(b *testing.B) {
			capture := Capture{DistinctId: "user_1", Event: "test_event", Properties: generatePropertiesWithCardinality(42, card), Groups: generateGroupsWithCardinality(42, card)}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkMessageSink = capture.APIfy()
			}
		})
	}
}

func BenchmarkMessageEnqueueOverhead(b *testing.B) {
	benchmarkEnqueue(b, NewEventPoolWithCardinality(1000, CardinalityLow), 0)
}

func BenchmarkCompressionOverhead(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		capture := NewEventPoolWithCardinality(1, card).Next()
		raw, _, err := prepareForSend(capture)
		if err != nil {
			b.Fatal(err)
		}
		for _, mode := range []CompressionMode{CompressionNone, CompressionGzip} {
			b.Run(fmt.Sprintf("%d/%s", mode, CardinalityName(card)), func(b *testing.B) {
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, _, err := compressV1Body(mode, raw); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkEndToEndWithCompression(b *testing.B) {
	for _, mode := range []CompressionMode{CompressionNone, CompressionGzip} {
		b.Run(fmt.Sprintf("compression_%d", mode), func(b *testing.B) {
			server := httptest.NewServer(NoOpHandler())
			defer server.Close()
			benchmarkDelivery(b, Config{Endpoint: server.URL, Compression: mode, MaxRetries: Ptr(0)}, NewEventPool(1000))
		})
	}
}

func BenchmarkCompressionRatio(b *testing.B) {
	for _, card := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		b.Run(CardinalityName(card), func(b *testing.B) {
			capture := NewEventPoolWithCardinality(1, card).Next()
			raw, _, err := prepareForSend(capture)
			if err != nil {
				b.Fatal(err)
			}
			var compressed []byte
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				compressed, err = gzipCompress(raw)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(100*float64(len(compressed))/float64(len(raw)), "compressed%")
		})
	}
}
