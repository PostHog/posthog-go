package posthog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// BenchmarkJSONMarshalBatch benchmarks JSON serialization at various batch sizes
func BenchmarkJSONMarshalBatch(b *testing.B) {
	sizes := []int{1, 10, 50, 100, 250}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			// Pre-generate batch
			msgs := make([]APIMessage, size)
			for i := range msgs {
				msgs[i] = generateVariedCapture(i).APIfy()
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
			// Pre-generate batch with specific cardinality
			msgs := make([]APIMessage, batchSize)
			for i := range msgs {
				capture := Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}
				msgs[i] = capture.APIfy()
			}
			payload := batch{ApiKey: "test", Messages: msgs}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				json.Marshal(payload)
			}
		})
	}
}

// BenchmarkEstimateJSONSize benchmarks the internal size estimation function
func BenchmarkEstimateJSONSize(b *testing.B) {
	props := generateVariedProperties(42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		estimateJSONSize(props)
	}
}

// BenchmarkEstimatedSize_Cardinality benchmarks EstimatedSize at various property cardinalities
// This validates that size estimation isn't a bottleneck
func BenchmarkEstimatedSize_Cardinality(b *testing.B) {
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
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				captures[i%100].EstimatedSize()
			}
		})
	}
}

// BenchmarkEstimateJSONSize_PropertyTypes benchmarks size estimation for various property types
func BenchmarkEstimateJSONSize_PropertyTypes(b *testing.B) {
	testCases := []struct {
		name  string
		props Properties
	}{
		{"strings_only", Properties{"a": "short", "b": strings.Repeat("x", 100), "c": strings.Repeat("y", 1000)}},
		{"numbers_only", Properties{"int": 12345, "float": 3.14159, "big": 999999999}},
		{"mixed_low", generatePropertiesWithCardinality(42, CardinalityLow)},
		{"mixed_medium", generatePropertiesWithCardinality(42, CardinalityMedium)},
		{"nested_maps", Properties{
			"user": map[string]interface{}{
				"id": 1,
				"profile": map[string]interface{}{
					"name": "test",
					"settings": map[string]interface{}{"theme": "dark"},
				},
			},
		}},
		{"arrays", Properties{
			"tags": []string{"a", "b", "c", "d", "e"},
			"ids":  []interface{}{1, 2, 3, 4, 5},
		}},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				estimateJSONSize(tc.props)
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
			// Pre-generate and serialize payload
			msgs := make([]APIMessage, 10)
			for i := range msgs {
				capture := Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
				}
				msgs[i] = capture.APIfy()
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

// BenchmarkSizeEstimationVsActualMarshal compares the cost of estimation vs actual marshaling
func BenchmarkSizeEstimationVsActualMarshal(b *testing.B) {
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
			DistinctId: "user_1",
			Event:      "test_event",
			Properties: generatePropertiesWithCardinality(42, tc.cardinality),
			Groups:     generateGroupsWithCardinality(42, tc.cardinality),
		}
		apiMsg := capture.APIfy()

		b.Run(tc.name+"_estimate", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				capture.EstimatedSize()
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

