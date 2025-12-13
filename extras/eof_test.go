package extras

import (
	"context"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/orian/flakyhttp"
	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/require"
)

type timeoutTransport struct {
	rt      http.RoundTripper
	timeout time.Duration
}

func (t *timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), t.timeout)
	defer cancel()
	return t.rt.RoundTrip(req.WithContext(ctx))
}

// testCallback implements posthog.Callback to capture errors
type testCallback struct {
	t       *testing.T
	errChan chan error
}

func (c *testCallback) Success(msg posthog.APIMessage) {
	c.t.Logf("Callback success for message: %v", msg)
}

func (c *testCallback) Failure(msg posthog.APIMessage, err error) {
	c.t.Logf("Callback failure: %v", err)
	if c.errChan != nil {
		c.errChan <- err
	}
}

func TestPostHogHeadersOnlyClose(t *testing.T) {
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:        flakyhttp.ScenarioHeadersOnlyClose,
		PartialBodySize: 10, // Send 10 bytes then close (promised 1000)
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s", url)

	// Create PostHog client pointing to mock server with same config as eof.go
	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  100 * time.Millisecond, // Fast flush for testing
			BatchSize: 1,                      // Send immediately after 1 event
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Enqueue an event - this will trigger a batch send to the mock server
	err = client.Enqueue(posthog.Capture{
		DistinctId: "testuser",
		Event:      "test event",
		Properties: map[string]interface{}{
			"hello": "world",
		},
	})
	require.NoError(t, err, "Failed to enqueue event")
	t.Log("Event enqueued, waiting for batch send...")

	// Wait for the batch to be sent and processed
	time.Sleep(500 * time.Millisecond)

	// Close the client - this will flush any remaining events
	err = client.Close()
	if err != nil {
		t.Logf("Client close error (may indicate EOF error): %v", err)
	}

	t.Logf("Server received %d request(s)", server.RequestCount())
}

func TestPostHogHeadersOnlyCloseWithCallback(t *testing.T) {
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:        flakyhttp.ScenarioHeadersOnlyClose,
		PartialBodySize: 0, // Send 0 bytes of body (promised 1000) - forces EOF on first read
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s", url)

	// Channel to capture callback errors
	errChan := make(chan error, 10)

	// Create PostHog client with callback to capture errors
	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  100 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  &testCallback{t: t, errChan: errChan},
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Enqueue an event
	err = client.Enqueue(posthog.Capture{
		DistinctId: "testuser",
		Event:      "test event",
		Properties: map[string]interface{}{
			"hello": "world",
		},
	})
	require.NoError(t, err, "Failed to enqueue event")
	t.Log("Event enqueued, waiting for callback...")

	// Wait for callback with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	select {
	case capturedErr := <-errChan:
		t.Logf("Captured error via callback: %v", capturedErr)
	case <-ctx.Done():
		t.Log("No error captured within timeout (PostHog client may have handled it)")
	}

	client.Close()
	t.Logf("Server received %d request(s)", server.RequestCount())
}

// TestPostHogNoResponse tests the scenario where server closes connection without any response.
// This should produce the exact "unexpected EOF" error.
func TestPostHogNoResponse(t *testing.T) {
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario: flakyhttp.ScenarioNoResponse,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s", url)

	// Channel to capture callback errors
	errChan := make(chan error, 10)

	// Create PostHog client with callback to capture errors
	// Use RetryAfter: -1 to disable retries so we get the error faster
	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:   100 * time.Millisecond,
			BatchSize:  1,
			RetryAfter: func(i int) time.Duration { return -1 }, // Disable retries
			Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:   &testCallback{t: t, errChan: errChan},
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Enqueue an event
	err = client.Enqueue(posthog.Capture{
		DistinctId: "testuser",
		Event:      "test event",
		Properties: map[string]interface{}{
			"hello": "world",
		},
	})
	require.NoError(t, err, "Failed to enqueue event")
	t.Log("Event enqueued, waiting for callback...")

	// Wait for callback with timeout (longer to allow for retries if RetryAfter doesn't work)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	select {
	case capturedErr := <-errChan:
		t.Logf("Captured expected EOF error via callback: %v", capturedErr)
	case <-ctx.Done():
		require.Fail(t, "Expected an error but none was captured within timeout")
	}

	client.Close()
	t.Logf("Server received %d request(s)", server.RequestCount())
}

// TestPostHogStatusOnly tests the scenario where server sends "HTTP/1.1 200 OK\r\n" then closes.
func TestPostHogStatusOnly(t *testing.T) {
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario: flakyhttp.ScenarioStatusOnly,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s", url)

	// Channel to capture callback errors
	errChan := make(chan error, 10)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:   100 * time.Millisecond,
			BatchSize:  1,
			RetryAfter: func(i int) time.Duration { return -1 },
			Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:   &testCallback{t: t, errChan: errChan},
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "testuser",
		Event:      "test event",
		Properties: map[string]interface{}{
			"hello": "world",
		},
	})
	require.NoError(t, err, "Failed to enqueue event")
	t.Log("Event enqueued, waiting for callback...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	select {
	case capturedErr := <-errChan:
		t.Logf("Captured error via callback: %v", capturedErr)
	case <-ctx.Done():
		t.Log("No error captured - PostHog client handled it successfully")
	}

	client.Close()
	t.Logf("Server received %d request(s)", server.RequestCount())
}
