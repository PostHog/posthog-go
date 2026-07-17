package posthog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newQueueTestClient builds a client whose consumer loop is deliberately NOT
// started. That lets a test fill the msgs channel to capacity and be certain
// nothing ever drains it, which is the only way to exercise the Enqueue
// drop-on-full path deterministically: a running consumer drains msgs into
// per-batch goroutines faster than a single-threaded producer can overflow it,
// so overflow is otherwise reachable only via a producer/consumer race.
func newQueueTestClient(capacity int, logger Logger, callback Callback) *client {
	c := &client{
		Config: Config{
			now:      time.Now,
			Logger:   logger,
			Callback: callback,
		},
		msgs: make(chan preparedMessage, capacity),
	}
	c.capture = legacyCapturer{c}
	return c
}

func captureOverflowEvent(event string) Capture {
	return Capture{
		Event:            event,
		DistinctId:       "user-1",
		SendFeatureFlags: SendFeatureFlags(false),
	}
}

func TestEnqueue_DropsNewestWhenQueueFull(t *testing.T) {
	const capacity = 4

	var failures []error
	var warnings []string

	logger := testLogger{logf: func(format string, args ...interface{}) {
		warnings = append(warnings, format)
	}}
	callback := testCallback{failure: func(_ APIMessage, err error) {
		failures = append(failures, err)
	}}

	c := newQueueTestClient(capacity, logger, callback)

	// Saturate the queue so no further message can be buffered.
	for i := 0; i < capacity; i++ {
		c.msgs <- preparedMessage{}
	}

	// Enqueue must not block even though the queue is full and has no consumer;
	// a blocking-send implementation would hang here forever.
	done := make(chan error, 1)
	go func() {
		done <- c.Enqueue(captureOverflowEvent("overflow"))
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a full queue; drop-on-full must be non-blocking")
	}

	require.ErrorIs(t, err, ErrQueueFull, "Enqueue must return ErrQueueFull when the queue is full")
	require.Len(t, c.msgs, capacity, "the dropped message must not be buffered")

	// The drop is reported only through the returned error. Doing callback or log
	// work here would run on the caller's goroutine, re-introducing the latency and
	// re-entrancy the non-blocking drop path exists to avoid.
	require.Empty(t, failures, "Callback.Failure must not run on the caller's Enqueue goroutine")
	require.Empty(t, warnings, "the drop path must not log synchronously on the caller's goroutine")
}

func TestEnqueue_BuffersWhenQueueHasRoom(t *testing.T) {
	var failures int

	callback := testCallback{failure: func(APIMessage, error) { failures++ }}
	c := newQueueTestClient(2, testLogger{}, callback)

	require.NoError(t, c.Enqueue(captureOverflowEvent("buffered")))
	require.Len(t, c.msgs, 1, "a message enqueued with room to spare must be buffered, not dropped")
	require.Zero(t, failures, "no failure callback should fire on the happy path")
}
