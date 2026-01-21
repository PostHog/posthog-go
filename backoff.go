package posthog

import (
	"math"
	"math/rand"
	"time"
)

type Backoff struct {
	base   time.Duration
	factor uint8
	jitter float64
	cap    time.Duration
}

// Creates a backo instance with the given parameters
func NewBackoff(base time.Duration, factor uint8, jitter float64, cap time.Duration) *Backoff {
	return &Backoff{base, factor, jitter, cap}
}

// Creates a backoff instance with the following defaults:
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

type Ticker struct {
	done chan struct{}
	C    <-chan time.Time
}

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

func (t *Ticker) Stop() {
	t.done <- struct{}{}
}
