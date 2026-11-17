package posthog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoffJitterScalesDelayWithinBounds(t *testing.T) {
	const base = 100 * time.Millisecond
	cases := []struct {
		name    string
		random  float64
		attempt int
		want    time.Duration
	}{
		{"lowest_draw_is_three_quarters", 0, 0, 75 * time.Millisecond},
		{"middle_draw_is_unchanged", 0.5, 1, 200 * time.Millisecond},
		{"highest_draw_is_five_quarters", 1, 2, 500 * time.Millisecond},
		{"cap_applies_after_jitter", 1, 3, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBackoff(base, 2, 0.25, time.Second)
			b.random = func() float64 { return tc.random }
			require.Equal(t, tc.want, b.Duration(tc.attempt))
		})
	}
}

func TestDefaultRetryAfterIsJittered(t *testing.T) {
	retryAfter := makeConfig(Config{}).RetryAfter
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := retryAfter(1)
		require.GreaterOrEqual(t, d, 150*time.Millisecond)
		require.LessOrEqual(t, d, 250*time.Millisecond)
		seen[d] = true
	}
	require.Greater(t, len(seen), 1, "default retry waits must vary between clients")
}

func TestRetryDelayJitterNeverShortensRetryAfter(t *testing.T) {
	c := &client{Config: makeConfig(Config{})}
	res := &attemptResult{retryAfter: 120 * time.Millisecond, hasRetryAfter: true}
	for i := 0; i < 200; i++ {
		d := c.retryDelay(0, res)
		require.GreaterOrEqual(t, d, 120*time.Millisecond)
		require.LessOrEqual(t, d, 125*time.Millisecond)
	}
}
