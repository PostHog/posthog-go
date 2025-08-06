package posthog

import (
	"fmt"
	"math"
	"testing"
)

func TestCalculateHash(t *testing.T) {
	for _, tt := range []struct {
		ident string
		want  float64
	}{
		{"some_distinct_id", 0.7270002403585725},
		{"test-identifier", 0.4493881716040236},
		{"example_id", 0.9402003475831224},
		{"example_id2", 0.6292740389966519},
	} {
		t.Run(tt.ident, func(t *testing.T) {
			got := calculateHash("holdout-", tt.ident, "")
			if math.Abs(got-tt.want) > 0.000001 {
				t.Logf("got: %.16f, want: %f", got, tt.want)
				t.Fail()
			}
		})
	}
}

func TestArgumentConversion(t *testing.T) {
	for _, tt := range []struct {
		input    interface{}
		expected float64
		error    bool
	}{
		{42, 42.0, false},
		{3.14, 3.14, false},
		{"123.456", 123.456, false},
		{"not_a_number", 0.0, true}, // This should fail
	} {
		t.Run(fmt.Sprintf("Converting %v...", tt.input), func(t *testing.T) {
			result, err := interfaceToFloat(tt.input)
			if tt.error != (err != nil) {
				t.Errorf("Expected error: %v, got: %v", tt.error, err)
			}
			if !tt.error && result != tt.expected {
				t.Errorf("Expected: %f, got: %f", tt.expected, result)
			}
		})
	}
}
