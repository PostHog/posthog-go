package posthog

import (
	"testing"
	"time"
)

func awaitTestValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for asynchronous test observation")
		var zero T
		return zero
	}
}
