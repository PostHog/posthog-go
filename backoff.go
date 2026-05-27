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
}

// NewBackoff creates a Backoff with the provided base duration, exponential
// factor, jitter ratio, and maximum cap duration.
func NewBackoff(base time.Duration, factor uint8, jitter float64, cap time.Duration) *Backoff {
	return &Backoff{base, factor, jitter, cap}
}

// DefaultBackoff creates a Backoff with the SDK's default retry policy:
//
//	base: 100 milliseconds
//	factor: 2
//	jitter: 0
//	cap: 10 seconds
func DefaultBackoff() *Backoff {
	return NewBackoff(time.Millisecond*100, 2, 0, time.Second*10)
}

// Duration returns the backoff interval for the given attempt.
func (b *Backoff) Duration(attempt int) time.Duration {
	duration := float64(b.base) * math.Pow(float64(b.factor), float64(attempt))

	if b.jitter != 0 {
		random := rand.Float64()
		deviation := math.Floor(random * b.jitter * duration)
		if (int(math.Floor(random*10)) & 1) == 0 {
			duration = duration - deviation
		} else {
			duration = duration + deviation
		}
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
