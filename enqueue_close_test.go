package posthog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnqueueReturnsErrClosedIfCloseStartsDuringBeforeSend(t *testing.T) {
	release := make(chan struct{})
	done := make(chan error, 1)

	client, err := NewWithConfig("test-key", Config{
		Endpoint:  "http://127.0.0.1:9",
		Interval:  time.Hour,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			<-release
			return msg
		},
	})
	require.NoError(t, err)

	go func() {
		done <- client.Enqueue(Capture{DistinctId: "u", Event: "e"})
	}()

	time.Sleep(20 * time.Millisecond)
	require.NoError(t, client.Close())
	close(release)

	require.ErrorIs(t, <-done, ErrClosed)
}
