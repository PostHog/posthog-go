package posthog

import (
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
