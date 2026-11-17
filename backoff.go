package posthog

import (
	"math"
	"math/rand"
	"time"
)

// Backoff calculates exponential retry delays with optional jitter and a maximum cap.
type Backoff struct {
	base   time.Duration
	factor uint8
	jitter float64
	cap    time.Duration
	random func() float64
}

// NewBackoff creates a Backoff with the provided base duration, exponential
// factor, jitter ratio, and maximum cap duration. A jitter ratio j scales each
// delay by a uniform random factor in [1-j, 1+j] before the cap applies.
func NewBackoff(base time.Duration, factor uint8, jitter float64, cap time.Duration) *Backoff {
	return &Backoff{base: base, factor: factor, jitter: jitter, cap: cap, random: rand.Float64}
}

const (
	// DefaultMaxRetryBackoff is the default ceiling for a single retry wait,
	// used when Config.MaxRetryBackoff is zero. Matches posthog-rs and
	// posthog-python.
	DefaultMaxRetryBackoff = 30 * time.Second

	defaultBackoffBase   = 100 * time.Millisecond
	defaultBackoffFactor = 2
	// defaultBackoffJitter spreads retries from many clients that failed
	// together, so they don't all return to the server at the same moment.
	defaultBackoffJitter = 0.25
)

// DefaultBackoff creates a Backoff with the SDK's default retry policy:
//
//	base: 100 milliseconds
//	factor: 2
//	jitter: 0.25 (each delay is scaled by a random factor in [0.75, 1.25])
//	cap: 30 seconds
func DefaultBackoff() *Backoff {
	return NewBackoff(defaultBackoffBase, defaultBackoffFactor, defaultBackoffJitter, DefaultMaxRetryBackoff)
}

// Duration returns the backoff interval for the given attempt.
func (b *Backoff) Duration(attempt int) time.Duration {
	duration := float64(b.base) * math.Pow(float64(b.factor), float64(attempt))

	if b.jitter != 0 {
		duration *= 1 + b.jitter*(2*b.random()-1)
	}

	duration = math.Min(float64(duration), float64(b.cap))
	return time.Duration(duration)
}

// Sleep pauses the current goroutine for the backoff interval for the given attempt.
func (b *Backoff) Sleep(attempt int) {
	duration := b.Duration(attempt)
	time.Sleep(duration)
}

// Ticker delivers ticks using successive durations from a Backoff.
type Ticker struct {
	done chan struct{}
	// C receives the time for each backoff tick and is closed after Stop.
	C <-chan time.Time
}

// NewTicker starts a ticker that waits Duration(0), Duration(1), and so on between ticks.
func (b *Backoff) NewTicker() *Ticker {
	c := make(chan time.Time, 1)
	ticker := &Ticker{
		done: make(chan struct{}, 1),
		C:    c,
	}

	go func() {
		for i := 0; ; i++ {
			timer := time.NewTimer(b.Duration(i))
			select {
			case t := <-timer.C:
				c <- t
			case <-ticker.done:
				timer.Stop()
				close(c)
				return
			}
		}
	}()

	return ticker
}

// Stop stops the ticker goroutine and closes C. Stop should be called at most once.
func (t *Ticker) Stop() {
	t.done <- struct{}{}
}
