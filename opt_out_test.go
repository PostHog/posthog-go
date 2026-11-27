package posthog

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// optOutServer answers both capture lanes and counts the requests each one
// received, so a test can tell "nothing was captured" from "captured late".
func optOutServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		serveCaptureOK(w, r)
	}))
}

func newOptOutClient(t *testing.T, endpoint string) Client {
	t.Helper()
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  endpoint,
		BatchSize: 1,
		Verbose:   true,
		Logger:    testLogger{logf: func(string, ...interface{}) {}, errorf: func(string, ...interface{}) {}},
		now:       mockTime,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestClientStartsOptedIn(t *testing.T) {
	var requests atomic.Int64
	server := optOutServer(t, &requests)
	defer server.Close()

	client := newOptOutClient(t, server.URL)
	controller, ok := client.(OptOutController)
	require.True(t, ok, "the client implements OptOutController")
	require.False(t, controller.IsOptedOut())

	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: "Captured Event"}))
	require.NoError(t, client.Flush())
	require.Equal(t, int64(1), requests.Load())
}

func TestOptOutDropsCaptureSilently(t *testing.T) {
	var requests atomic.Int64
	server := optOutServer(t, &requests)
	defer server.Close()

	client := newOptOutClient(t, server.URL)
	controller := client.(OptOutController)
	controller.OptOutCapturing()
	require.True(t, controller.IsOptedOut())

	// Every lane and every message type is gated, and none of them is an
	// error the caller has to handle.
	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: "Ignored Event"}))
	require.NoError(t, client.Enqueue(Identify{DistinctId: "user-123"}))
	require.NoError(t, client.Enqueue(Alias{DistinctId: "user-123", Alias: "user-456"}))
	require.NoError(t, client.Enqueue(GroupIdentify{Type: "company", Key: "acme"}))
	require.NoError(t, client.EnqueueAI(Capture{DistinctId: "user-123", Event: "$ai_generation"}))
	require.NoError(t, client.Flush())

	require.Equal(t, int64(0), requests.Load(), "no capture request is sent while opted out")
}

func TestOptOutSkipsValidation(t *testing.T) {
	var requests atomic.Int64
	server := optOutServer(t, &requests)
	defer server.Close()

	client := newOptOutClient(t, server.URL)
	client.(OptOutController).OptOutCapturing()

	// An opted-out client short-circuits before validation, so a message that
	// would normally fail returns nil rather than a validation error.
	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: ""}))
	require.Equal(t, int64(0), requests.Load())
}

func TestOptInResumesCapture(t *testing.T) {
	var requests atomic.Int64
	server := optOutServer(t, &requests)
	defer server.Close()

	client := newOptOutClient(t, server.URL)
	controller := client.(OptOutController)

	controller.OptOutCapturing()
	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: "Ignored Event"}))

	controller.OptInCapturing()
	require.False(t, controller.IsOptedOut())
	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: "Captured Event"}))
	require.NoError(t, client.Flush())

	require.Equal(t, int64(1), requests.Load(), "only the event captured after opt-in is sent")
}

func TestOptOutKeepsAlreadyQueuedEventsDeliverable(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		serveCaptureOK(w, r)
	}))
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 100,
		Interval:  time.Hour,
		Logger:    testLogger{logf: func(string, ...interface{}) {}, errorf: func(string, ...interface{}) {}},
		now:       mockTime,
	})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Enqueue(Capture{DistinctId: "user-123", Event: "Accepted Event"}))
	client.(OptOutController).OptOutCapturing()
	require.NoError(t, client.Flush())

	require.Equal(t, int64(1), requests.Load(), "opt-out stops new capture, it does not discard accepted events")
}

func TestNoopClientIsAlwaysOptedOut(t *testing.T) {
	client, err := NewWithConfig("", Config{})
	require.NoError(t, err)
	defer client.Close()

	controller, ok := client.(OptOutController)
	require.True(t, ok, "the disabled client implements OptOutController")
	require.True(t, controller.IsOptedOut(), "a client without a project API key reads as opted out")

	controller.OptInCapturing()
	require.True(t, controller.IsOptedOut(), "opting in cannot turn on a client without a project API key")
}
