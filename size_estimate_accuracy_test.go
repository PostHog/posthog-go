package posthog

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSizeEstimationAccuracy verifies estimates are within acceptable margin
// of actual JSON-encoded size across various property cardinalities
func TestSizeEstimationAccuracy(t *testing.T) {
	// Acceptable margin: estimate should be within 20% of actual size
	const maxMargin = 0.20

	cardinalities := []struct {
		name        string
		cardinality PropertyCardinality
	}{
		{"low_cardinality", CardinalityLow},
		{"medium_cardinality", CardinalityMedium},
		{"high_cardinality", CardinalityHigh},
	}

	for _, tc := range cardinalities {
		t.Run(tc.name, func(t *testing.T) {
			// Test multiple samples at this cardinality
			for i := 0; i < 50; i++ {
				capture := Capture{
					DistinctId: fmt.Sprintf("user_%d", i),
					Event:      "test_event",
					Timestamp:  time.Now(),
					Properties: generatePropertiesWithCardinality(i, tc.cardinality),
					Groups:     generateGroupsWithCardinality(i, tc.cardinality),
				}

				estimated := capture.EstimatedSize()

				// Get actual JSON size
				apiMsg := capture.APIfy()
				jsonBytes, err := json.Marshal(apiMsg)
				require.NoError(t, err)
				actual := len(jsonBytes)

				margin := math.Abs(float64(estimated-actual)) / float64(actual)
				if margin > maxMargin {
					t.Errorf("Sample %d: Estimate %d differs from actual %d by %.1f%% (max %.1f%%)",
						i, estimated, actual, margin*100, maxMargin*100)
				}
			}
		})
	}
}

// TestSizeEstimationPropertyTypes verifies estimation for various value types
// Note: Size estimation is designed to be conservative (overestimate) to prevent
// batch size overflow. For small values, the relative margin can be high but the
// absolute error is still small.
func TestSizeEstimationPropertyTypes(t *testing.T) {
	testCases := []struct {
		name      string
		value     interface{}
		maxMargin float64 // Different values may need different tolerances
	}{
		{"short_string", "hello", 0.30},
		{"long_string", strings.Repeat("x", 1000), 0.15},
		{"unicode_string", "こんにちは世界🌍", 0.50}, // Unicode estimation is tricky
		{"integer", 12345, 1.50},                     // Small ints have high relative margin but small absolute error
		{"large_integer", 999999999999, 0.30},
		{"float", 3.14159265358979, 0.50},
		{"boolean_true", true, 1.50},   // 4 bytes vs estimate ~5
		{"boolean_false", false, 1.50}, // 5 bytes vs estimate ~5
		{"nil", nil, 1.50},             // 4 bytes (null) vs estimate
		{"string_array", []string{"a", "bb", "ccc"}, 0.50},
		{"nested_map", map[string]interface{}{"a": map[string]interface{}{"b": "c"}}, 0.50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			estimated := estimateJSONSize(tc.value)

			jsonBytes, err := json.Marshal(tc.value)
			require.NoError(t, err)
			actual := len(jsonBytes)

			// Handle edge case where actual is 0
			divisor := float64(actual)
			if divisor == 0 {
				divisor = 1
			}

			margin := math.Abs(float64(estimated-actual)) / divisor

			// For small values, also check absolute error is reasonable (< 20 bytes)
			absError := math.Abs(float64(estimated - actual))
			if margin > tc.maxMargin && absError > 20 {
				t.Errorf("Type %T: estimate %d differs from actual %d by %.1f%% (abs: %.0f bytes)",
					tc.value, estimated, actual, margin*100, absError)
			}
		})
	}
}

// TestSizeEstimationGroupProperties verifies group property estimation
func TestSizeEstimationGroupProperties(t *testing.T) {
	cardinalities := []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh}

	for _, cardinality := range cardinalities {
		t.Run(CardinalityName(cardinality), func(t *testing.T) {
			groups := generateGroupsWithCardinality(42, cardinality)

			estimated := estimateJSONSize(groups)
			jsonBytes, err := json.Marshal(groups)
			require.NoError(t, err)
			actual := len(jsonBytes)

			margin := math.Abs(float64(estimated-actual)) / float64(actual)
			if margin > 0.25 {
				t.Errorf("Groups estimate %d differs from actual %d by %.1f%%",
					estimated, actual, margin*100)
			}
		})
	}
}

// TestSizeEstimationConsistency verifies that size estimation is consistent
func TestSizeEstimationConsistency(t *testing.T) {
	capture := Capture{
		DistinctId: "test_user",
		Event:      "test_event",
		Timestamp:  time.Now(),
		Properties: generatePropertiesWithCardinality(42, CardinalityMedium),
		Groups:     generateGroupsWithCardinality(42, CardinalityMedium),
	}

	// Call EstimatedSize multiple times
	first := capture.EstimatedSize()
	for i := 0; i < 100; i++ {
		got := capture.EstimatedSize()
		if got != first {
			t.Errorf("Inconsistent size estimation: first=%d, got=%d on iteration %d",
				first, got, i)
		}
	}
}

// TestSizeEstimationPropertiesMap verifies estimation of Properties type specifically
// Size estimation is designed to be conservative for safety, so we allow for overestimation
func TestSizeEstimationPropertiesMap(t *testing.T) {
	testCases := []struct {
		name      string
		props     Properties
		maxMargin float64
	}{
		{"empty", Properties{}, 2.0},
		{"single_string", Properties{"key": "value"}, 0.50},
		{"single_int", Properties{"count": 42}, 1.50},   // Small values have higher relative margin
		{"single_float", Properties{"price": 19.99}, 0.50},
		{"single_bool", Properties{"active": true}, 1.00},
		{"mixed_small", Properties{
			"name":   "test",
			"count":  100,
			"active": true,
		}, 0.50},
		{"mixed_medium", generatePropertiesWithCardinality(1, CardinalityLow), 0.25},
		{"nested", Properties{
			"user": map[string]interface{}{
				"id":   1,
				"name": "test",
				"meta": map[string]interface{}{
					"created": "2024-01-01",
					"updated": "2024-01-02",
				},
			},
		}, 0.50},
		{"arrays", Properties{
			"tags":   []string{"a", "b", "c", "d", "e"},
			"ids":    []interface{}{1, 2, 3, 4, 5},
			"scores": []interface{}{1.1, 2.2, 3.3},
		}, 1.50}, // Arrays have conservative estimation
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			estimated := estimateJSONSize(tc.props)
			jsonBytes, err := json.Marshal(tc.props)
			require.NoError(t, err)
			actual := len(jsonBytes)

			// Handle empty case - estimate should be small
			if actual <= 2 {
				if estimated > 10 {
					t.Errorf("Near-empty properties: estimated %d for actual %d", estimated, actual)
				}
				return
			}

			margin := math.Abs(float64(estimated-actual)) / float64(actual)
			absError := math.Abs(float64(estimated - actual))

			// For small payloads, also check absolute error is reasonable
			if margin > tc.maxMargin && absError > 30 {
				t.Errorf("Properties estimate %d differs from actual %d by %.1f%% (abs: %.0f)",
					estimated, actual, margin*100, absError)
			}
		})
	}
}

// TestSizeEstimationLargePayloads verifies estimation works for very large payloads
func TestSizeEstimationLargePayloads(t *testing.T) {
	// Test at the high end of cardinality range
	for seed := 0; seed < 10; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			props := generatePropertiesWithCardinality(seed, CardinalityHigh)
			capture := Capture{
				DistinctId: fmt.Sprintf("user_%d", seed),
				Event:      "large_event",
				Timestamp:  time.Now(),
				Properties: props,
				Groups:     generateGroupsWithCardinality(seed, CardinalityHigh),
			}

			estimated := capture.EstimatedSize()
			apiMsg := capture.APIfy()
			jsonBytes, err := json.Marshal(apiMsg)
			require.NoError(t, err)
			actual := len(jsonBytes)

			// For large payloads, allow slightly more margin (25%)
			margin := math.Abs(float64(estimated-actual)) / float64(actual)
			if margin > 0.25 {
				t.Errorf("Large payload: estimate %d differs from actual %d by %.1f%%",
					estimated, actual, margin*100)
			}

			t.Logf("Seed %d: props=%d, estimated=%d, actual=%d, margin=%.1f%%",
				seed, len(props), estimated, actual, margin*100)
		})
	}
}

// TestSizeEstimationEdgeCases verifies estimation handles edge cases
func TestSizeEstimationEdgeCases(t *testing.T) {
	t.Run("nil_properties", func(t *testing.T) {
		capture := Capture{
			DistinctId: "test",
			Event:      "test",
			Properties: nil,
		}
		size := capture.EstimatedSize()
		if size <= 0 {
			t.Error("EstimatedSize should return positive value even with nil properties")
		}
	})

	t.Run("nil_groups", func(t *testing.T) {
		capture := Capture{
			DistinctId: "test",
			Event:      "test",
			Groups:     nil,
		}
		size := capture.EstimatedSize()
		if size <= 0 {
			t.Error("EstimatedSize should return positive value even with nil groups")
		}
	})

	t.Run("empty_strings", func(t *testing.T) {
		capture := Capture{
			DistinctId: "",
			Event:      "",
			Properties: Properties{"": ""},
		}
		size := capture.EstimatedSize()
		if size <= 0 {
			t.Error("EstimatedSize should return positive value with empty strings")
		}
	})
}

