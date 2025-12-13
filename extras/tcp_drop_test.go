package extras

import (
	"log"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/orian/flakyhttp"
	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tcpTestCallback implements posthog.Callback for TCP drop tests
type tcpTestCallback struct {
	t            *testing.T
	mu           sync.Mutex
	successCount int
	failureCount int
	lastError    error
	successChan  chan posthog.APIMessage
	failureChan  chan error
}

func newTCPTestCallback(t *testing.T) *tcpTestCallback {
	return &tcpTestCallback{
		t:           t,
		successChan: make(chan posthog.APIMessage, 100),
		failureChan: make(chan error, 100),
	}
}

func (c *tcpTestCallback) Success(msg posthog.APIMessage) {
	c.mu.Lock()
	c.successCount++
	c.mu.Unlock()
	c.t.Logf("Callback: SUCCESS")
	c.successChan <- msg
}

func (c *tcpTestCallback) Failure(msg posthog.APIMessage, err error) {
	c.mu.Lock()
	c.failureCount++
	c.lastError = err
	c.mu.Unlock()
	c.t.Logf("Callback: FAILURE - %v", err)
	c.failureChan <- err
}

func (c *tcpTestCallback) GetCounts() (success, failure int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount
}

// TestTCPDropAfterConnect tests when server drops connection immediately after TCP accept.
func TestTCPDropAfterConnect(t *testing.T) {
	server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
		Scenario:  flakyhttp.TCPScenarioDropAfterConnect,
		FailCount: 3, // Fail 3 times, then succeed
		SendRST:   true,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start server")
	defer server.Close()
	t.Logf("TCP server started at %s", url)

	callback := newTCPTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 5 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "tcp_drop_after_connect",
	})
	require.NoError(t, err, "Failed to enqueue")

	// Wait for success (after retries)
	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after connection drop retries")
	case err := <-callback.failureChan:
		require.Fail(t, "Event failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure", success, failure)
	t.Logf("Server: %d connections, %d successful", server.ConnCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
}

// TestTCPDropAfterHeaders tests when server drops after reading request headers.
func TestTCPDropAfterHeaders(t *testing.T) {
	server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
		Scenario:  flakyhttp.TCPScenarioDropAfterHeaders,
		FailCount: 3,
		SendRST:   true,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start server")
	defer server.Close()
	t.Logf("TCP server started at %s", url)

	callback := newTCPTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 5 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "tcp_drop_after_headers",
	})
	require.NoError(t, err, "Failed to enqueue")

	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after header-drop retries")
	case err := <-callback.failureChan:
		require.Fail(t, "Event failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure", success, failure)
	t.Logf("Server: %d connections, %d successful", server.ConnCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
}

// TestTCPDropDuringBody tests when server drops while reading request body.
func TestTCPDropDuringBody(t *testing.T) {
	server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
		Scenario:  flakyhttp.TCPScenarioDropDuringBody,
		FailCount: 3,
		SendRST:   true,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start server")
	defer server.Close()
	t.Logf("TCP server started at %s", url)

	callback := newTCPTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 5 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "tcp_drop_during_body",
	})
	require.NoError(t, err, "Failed to enqueue")

	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after body-drop retries")
	case err := <-callback.failureChan:
		require.Fail(t, "Event failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure", success, failure)
	t.Logf("Server: %d connections, %d successful", server.ConnCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
}

// TestTCPDropAfterPartialBody tests when server drops after reading part of body.
func TestTCPDropAfterPartialBody(t *testing.T) {
	server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
		Scenario:    flakyhttp.TCPScenarioDropAfterPartialBody,
		BytesToRead: 50, // Read 50 bytes of body then drop
		FailCount:   3,
		SendRST:     true,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start server")
	defer server.Close()
	t.Logf("TCP server started at %s", url)

	callback := newTCPTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 5 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "tcp_drop_after_partial_body",
	})
	require.NoError(t, err, "Failed to enqueue")

	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after partial-body-drop retries")
	case err := <-callback.failureChan:
		require.Fail(t, "Event failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure", success, failure)
	t.Logf("Server: %d connections, %d successful", server.ConnCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
}

// TestTCPDropScenarios_ErrorMessages tests what error messages are produced by each scenario.
func TestTCPDropScenarios_ErrorMessages(t *testing.T) {
	scenarios := []struct {
		name     string
		scenario flakyhttp.TCPScenario
	}{
		{"DropAfterConnect", flakyhttp.TCPScenarioDropAfterConnect},
		{"DropAfterHeaders", flakyhttp.TCPScenarioDropAfterHeaders},
		{"DropDuringBody", flakyhttp.TCPScenarioDropDuringBody},
		{"DropAfterPartialBody", flakyhttp.TCPScenarioDropAfterPartialBody},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
				Scenario:    sc.scenario,
				BytesToRead: 50,
				FailCount:   0, // Always fail
				SendRST:     true,
			})

			url, err := server.Start()
			if err != nil {
				t.Fatalf("Failed to start server: %v", err)
			}
			defer server.Close()

			callback := newTCPTestCallback(t)

			client, err := posthog.NewWithConfig(
				"test-api-key",
				posthog.Config{
					Endpoint: url,
					Transport: &timeoutTransport{
						rt:      http.DefaultTransport,
						timeout: 5 * time.Second,
					},
					Interval:  50 * time.Millisecond,
					BatchSize: 1,
					RetryAfter: func(attempt int) time.Duration {
						return 10 * time.Millisecond // Fast retries
					},
					Logger:   posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
					Callback: callback,
				},
			)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			err = client.Enqueue(posthog.Capture{
				DistinctId: "user1",
				Event:      "test_error_message",
			})
			if err != nil {
				t.Fatalf("Failed to enqueue: %v", err)
			}

			// Wait for failure (after max retries)
			select {
			case err := <-callback.failureChan:
				t.Logf("Scenario %s produced error: %v", sc.name, err)
			case <-callback.successChan:
				require.Fail(t, "Unexpected success for scenario %s", sc.name)
			case <-time.After(10 * time.Second):
				require.Fail(t, "Timeout")
			}

			client.Close()
			t.Logf("Server received %d connections", server.ConnCount())
		})
	}
}

// TestTCPDropWithGracefulClose tests scenarios without RST (graceful FIN).
func TestTCPDropWithGracefulClose(t *testing.T) {
	server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
		Scenario:  flakyhttp.TCPScenarioDropAfterHeaders,
		FailCount: 0,     // Always fail
		SendRST:   false, // Graceful close (FIN)
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start server")
	defer server.Close()
	t.Logf("TCP server started at %s (graceful close)", url)

	callback := newTCPTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 5 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			RetryAfter: func(attempt int) time.Duration {
				return 10 * time.Millisecond
			},
			Logger:   posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback: callback,
		},
	)
	require.NoError(t, err, "Failed to create client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "graceful_close_test",
	})
	require.NoError(t, err, "Failed to enqueue")

	select {
	case err := <-callback.failureChan:
		t.Logf("Graceful close produced error: %v", err)
	case <-callback.successChan:
		require.Fail(t, "Unexpected success")
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout")
	}

	client.Close()
}
