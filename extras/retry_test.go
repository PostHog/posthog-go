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

// retryTestCallback implements posthog.Callback to track success/failure
type retryTestCallback struct {
	t            *testing.T
	mu           sync.Mutex
	successCount int
	failureCount int
	successChan  chan posthog.APIMessage
	failureChan  chan error
}

func newRetryTestCallback(t *testing.T) *retryTestCallback {
	return &retryTestCallback{
		t:           t,
		successChan: make(chan posthog.APIMessage, 100),
		failureChan: make(chan error, 100),
	}
}

func (c *retryTestCallback) Success(msg posthog.APIMessage) {
	c.mu.Lock()
	c.successCount++
	c.mu.Unlock()
	c.t.Logf("Callback: SUCCESS for event")
	c.successChan <- msg
}

func (c *retryTestCallback) Failure(msg posthog.APIMessage, err error) {
	c.mu.Lock()
	c.failureCount++
	c.mu.Unlock()
	c.t.Logf("Callback: FAILURE - %v", err)
	c.failureChan <- err
}

func (c *retryTestCallback) GetCounts() (success, failure int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount
}

// TestRetryOnEOF_EventualSuccess tests that PostHog retries on EOF and eventually succeeds.
func TestRetryOnEOF_EventualSuccess(t *testing.T) {
	// Server fails 3 times with EOF, then succeeds
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:  flakyhttp.ScenarioNoResponse,
		FailCount: 3,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s (will fail %d times then succeed)", url, 3)

	callback := newRetryTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Send one event
	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "test_retry_event",
	})
	require.NoError(t, err, "Failed to enqueue event")

	// Wait for success callback
	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after retries")
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for success callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure callbacks", success, failure)
	t.Logf("Server: %d requests received, %d successful responses", server.RequestCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
	assert.Equal(t, 0, failure, "Expected 0 failures (event should have been retried)")
	assert.Equal(t, 1, server.SuccessCount(), "Expected 1 successful server response")
}

// TestRetryOnUnexpectedEOF_EventualSuccess tests retry on "unexpected EOF" errors.
func TestRetryOnUnexpectedEOF_EventualSuccess(t *testing.T) {
	// Server fails 2 times with unexpected EOF (status only), then succeeds
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:  flakyhttp.ScenarioStatusOnly,
		FailCount: 2,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()
	t.Logf("Mock server started at %s (will fail %d times then succeed)", url, 2)

	callback := newRetryTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "test_unexpected_eof_retry",
	})
	require.NoError(t, err, "Failed to enqueue event")

	select {
	case <-callback.successChan:
		t.Log("Event delivered successfully after retries")
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for success callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure callbacks", success, failure)
	t.Logf("Server: %d requests received, %d successful responses", server.RequestCount(), server.SuccessCount())

	assert.Equal(t, 1, success, "Expected 1 success")
	assert.Equal(t, 0, failure, "Expected 0 failures")
}

// TestNoEventLoss_MultipleEvents tests that multiple events are all eventually delivered.
func TestNoEventLoss_MultipleEvents(t *testing.T) {
	// Server fails first 5 requests, then succeeds
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:  flakyhttp.ScenarioNoResponse,
		FailCount: 5,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()

	callback := newRetryTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1, // Send each event individually
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Send 3 events
	numEvents := 3
	for i := 0; i < numEvents; i++ {
		err = client.Enqueue(posthog.Capture{
			DistinctId: "user1",
			Event:      "test_event",
			Properties: map[string]interface{}{
				"event_number": i + 1,
			},
		})
		require.NoError(t, err, "Failed to enqueue event %d", i+1)
	}
	t.Logf("Enqueued %d events", numEvents)

	// Wait for all success callbacks
	successReceived := 0
	timeout := time.After(30 * time.Second)
	for successReceived < numEvents {
		select {
		case <-callback.successChan:
			successReceived++
			t.Logf("Received success %d/%d", successReceived, numEvents)
		case <-timeout:
			require.Fail(t, "Timeout: only received %d/%d success callbacks", successReceived, numEvents)
		}
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Final results: %d success, %d failure callbacks", success, failure)
	t.Logf("Server: %d requests received, %d successful responses", server.RequestCount(), server.SuccessCount())

	assert.Equal(t, numEvents, success, "Expected %d successes - EVENTS WERE LOST!", numEvents)
	assert.Equal(t, 0, failure, "Expected 0 failures - EVENTS WERE LOST!")
}

// TestNoEventLoss_BatchedEvents tests that batched events are all delivered after retries.
func TestNoEventLoss_BatchedEvents(t *testing.T) {
	// Server fails first 3 requests, then succeeds
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:  flakyhttp.ScenarioNoResponse,
		FailCount: 3,
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()

	callback := newRetryTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  100 * time.Millisecond,
			BatchSize: 5, // Batch 5 events together
			Logger:    posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback:  callback,
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	// Send 5 events (will be batched together)
	numEvents := 5
	for i := 0; i < numEvents; i++ {
		err = client.Enqueue(posthog.Capture{
			DistinctId: "user1",
			Event:      "batched_event",
			Properties: map[string]interface{}{
				"event_number": i + 1,
			},
		})
		require.NoError(t, err, "Failed to enqueue event %d", i+1)
	}
	t.Logf("Enqueued %d events (batch size: 5)", numEvents)

	// Wait for all success callbacks
	successReceived := 0
	timeout := time.After(30 * time.Second)
	for successReceived < numEvents {
		select {
		case <-callback.successChan:
			successReceived++
			t.Logf("Received success %d/%d", successReceived, numEvents)
		case <-timeout:
			require.Fail(t, "Timeout: only received %d/%d success callbacks", successReceived, numEvents)
		}
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Final results: %d success, %d failure callbacks", success, failure)
	t.Logf("Server: %d requests, %d successful", server.RequestCount(), server.SuccessCount())

	assert.Equal(t, numEvents, success, "Expected %d successes - EVENTS WERE LOST!", numEvents)
	assert.Equal(t, 0, failure, "Expected 0 failures - EVENTS WERE LOST!")
}

// TestEventLoss_ExceedsMaxRetries tests that events ARE lost when max retries is exceeded.
// This documents the current behavior - PostHog will drop events after 10 retries.
func TestEventLoss_ExceedsMaxRetries(t *testing.T) {
	// Server always fails (FailCount=0 means always fail)
	server := flakyhttp.NewServer(flakyhttp.Config{
		Scenario:  flakyhttp.ScenarioNoResponse,
		FailCount: 0, // Never succeed
	})

	url, err := server.Start()
	require.NoError(t, err, "Failed to start mock server")
	defer server.Close()

	callback := newRetryTestCallback(t)

	client, err := posthog.NewWithConfig(
		"test-api-key",
		posthog.Config{
			Endpoint: url,
			Transport: &timeoutTransport{
				rt:      http.DefaultTransport,
				timeout: 60 * time.Second,
			},
			Interval:  50 * time.Millisecond,
			BatchSize: 1,
			// Use fast retries to speed up the test
			RetryAfter: func(attempt int) time.Duration {
				return 10 * time.Millisecond
			},
			Logger:   posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
			Callback: callback,
		},
	)
	require.NoError(t, err, "Failed to create PostHog client")

	err = client.Enqueue(posthog.Capture{
		DistinctId: "user1",
		Event:      "doomed_event",
	})
	require.NoError(t, err, "Failed to enqueue event")

	// Wait for failure callback (after max retries)
	select {
	case err := <-callback.failureChan:
		t.Logf("Event was dropped after max retries: %v", err)
	case <-callback.successChan:
		require.Fail(t, "Unexpected success - server should never succeed")
	case <-time.After(10 * time.Second):
		require.Fail(t, "Timeout waiting for failure callback")
	}

	client.Close()

	success, failure := callback.GetCounts()
	t.Logf("Results: %d success, %d failure", success, failure)
	t.Logf("Server received %d requests (retries)", server.RequestCount())

	assert.Equal(t, 1, failure, "Expected 1 failure (event dropped)")
	assert.Equal(t, 0, success, "Expected 0 success")
	// PostHog retries 10 times by default
	assert.GreaterOrEqual(t, server.RequestCount(), 10, "Expected at least 10 retry attempts")
}
