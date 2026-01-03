package posthog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// BenchmarkJSONMarshalBatch benchmarks JSON serialization at various batch sizes
// Uses pre-serialized messages to match production behavior
func BenchmarkJSONMarshalBatch(b *testing.B) {
	sizes := []int{1, 10, 50, 100, 250}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			// Pre-generate and pre-serialize messages (as in production)
			msgs := make([]json.RawMessage, size)
			for i := range msgs {
				data, _ := json.Marshal(generateVariedCapture(i).APIfy())
				msgs[i] = json.RawMessage(data)
			}
			payload := batch{ApiKey: "test", Messages: msgs}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				json.Marshal(payload)
			}
		})
	}
}

// BenchmarkJSONMarshalBatchWithCardinality benchmarks JSON serialization with various property cardinalities
// Uses pre-serialized messages to match production behavior
func BenchmarkJSONMarshalBatchWithCardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_10-100_props", CardinalityLow},
		{"medium_500-2000_props", CardinalityMedium},
		{"high_2000-10000_props", CardinalityHigh},
	}

	batchSize := 10 // Use smaller batch for high cardinality to avoid excessive memory

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-generate and pre-serialize messages with specific cardinality
			msgs := make([]json.RawMessage, batchSize)
			for i := range msgs {
				capture := Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}
				data, _ := json.Marshal(capture.APIfy())
				msgs[i] = json.RawMessage(data)
			}
			payload := batch{ApiKey: "test", Messages: msgs}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				json.Marshal(payload)
			}
		})
	}
}

// BenchmarkPrepareForSend_Cardinality benchmarks prepareForSend at various property cardinalities
// This validates that the combined APIfy + serialization isn't a bottleneck
func BenchmarkPrepareForSend_Cardinality(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_10-100_props", CardinalityLow},
		{"medium_500-2000_props", CardinalityMedium},
		{"high_2000-10000_props", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-generate captures with this cardinality
			captures := make([]Capture, 100)
			for i := range captures {
				captures[i] = Capture{
					Type:       "capture",
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				captures[i%100].prepareForSend()
			}
		})
	}
}

// BenchmarkStructToMap benchmarks struct-to-map conversion
func BenchmarkStructToMap(b *testing.B) {
	capture := generateVariedCapture(42)
	v := reflect.ValueOf(capture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		structToMap(v, nil)
	}
}

// BenchmarkStructToMapWithCardinality benchmarks struct-to-map with various cardinalities
func BenchmarkStructToMapWithCardinality(b *testing.B) {
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
			v := reflect.ValueOf(capture)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				structToMap(v, nil)
			}
		})
	}
}

// BenchmarkHTTPUpload benchmarks HTTP upload isolated from serialization
func BenchmarkHTTPUpload(b *testing.B) {
	// Pre-serialize payloads
	payloadSizes := []int{1024, 10240, 102400} // 1KB, 10KB, 100KB
	for _, size := range payloadSizes {
		b.Run(fmt.Sprintf("payload_%dKB", size/1024), func(b *testing.B) {
			payload := make([]byte, size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
			}))
			defer server.Close()

			client := &http.Client{}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, _ := http.NewRequest("POST", server.URL, bytes.NewReader(payload))
				resp, _ := client.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
			}
		})
	}
}

// BenchmarkHTTPUploadWithRealPayload benchmarks HTTP upload with realistic JSON payloads
func BenchmarkHTTPUploadWithRealPayload(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_cardinality", CardinalityLow},
		{"medium_cardinality", CardinalityMedium},
	}

	for _, tc := range cardinalities {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-generate and pre-serialize messages (as in production)
			msgs := make([]json.RawMessage, 10)
			for i := range msgs {
				capture := Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
				}
				data, _ := json.Marshal(capture.APIfy())
				msgs[i] = json.RawMessage(data)
			}
			payload, _ := json.Marshal(batch{ApiKey: "test", Messages: msgs})

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
			}))
			defer server.Close()

			client := &http.Client{}

			b.ResetTimer()
			b.SetBytes(int64(len(payload)))
			for i := 0; i < b.N; i++ {
				req, _ := http.NewRequest("POST", server.URL, bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				resp, _ := client.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
			}
		})
	}
}

// BenchmarkPrepareVsMarshaling compares the cost of prepareForSend vs actual marshaling
func BenchmarkPrepareVsMarshaling(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		// Pre-generate capture
		capture := Capture{
			Type:       "capture",
			DistinctId: "user_1",
			Event:      "test_event",
			Properties: generatePropertiesWithCardinality(42, tc.cardinality),
			Groups:     generateGroupsWithCardinality(42, tc.cardinality),
		}
		apiMsg := capture.APIfy()

		b.Run(tc.name+"_prepare", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				capture.prepareForSend()
			}
		})

		b.Run(tc.name+"_marshal", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				json.Marshal(apiMsg)
			}
		})
	}
}

// BenchmarkPropertyIteration benchmarks the cost of iterating over properties
func BenchmarkPropertyIteration(b *testing.B) {
	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low", CardinalityLow},
		{"medium", CardinalityMedium},
		{"high", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		props := generatePropertiesWithCardinality(42, tc.cardinality)

		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				total := 0
				for k, v := range props {
					total += len(k)
					_ = v // access value
				}
			}
		})
	}
}

