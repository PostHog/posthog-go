package posthog

import (
	"math"
	"testing"
)

func TestHash(t *testing.T) {
	for _, tt := range []struct {
		ident string
		want  float64
	}{
		{"some_distinct_id", 0.086639},
		{"test-identifier", 0.749634},
		{"example_id", 0.869139},
		{"example_id2", 0.844273},
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
