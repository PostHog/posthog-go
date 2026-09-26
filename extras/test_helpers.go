package extras

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	posthog "github.com/posthog/posthog-go"
)

// timeoutTransport wraps an http.RoundTripper with a timeout.
type timeoutTransport struct {
	rt      http.RoundTripper
	timeout time.Duration
}

func (t *timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), t.timeout)
	res, err := t.rt.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return res, err
	}
	res.Body = &cancelOnCloseBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// testCallback implements posthog.Callback to track success/failure in tests.
type testCallback struct {
	t            *testing.T
	mu           sync.Mutex
	successCount int
	failureCount int
	lastError    error
	successChan  chan posthog.APIMessage
	failureChan  chan error
}

func newTestCallback(t *testing.T) *testCallback {
	return &testCallback{
		t:           t,
		successChan: make(chan posthog.APIMessage, 100),
		failureChan: make(chan error, 100),
	}
}

func (c *testCallback) Success(msg posthog.APIMessage) {
	c.mu.Lock()
	c.successCount++
	c.mu.Unlock()
	c.t.Logf("Callback: SUCCESS")
	c.successChan <- msg
}

func (c *testCallback) Failure(msg posthog.APIMessage, err error) {
	c.mu.Lock()
	c.failureCount++
	c.lastError = err
	c.mu.Unlock()
	c.t.Logf("Callback: FAILURE - %v", err)
	c.failureChan <- err
}

func (c *testCallback) GetCounts() (success, failure int) { return c.counts() }

func (c *testCallback) counts() (success, failure int) {
	c.mu.Lock()
	success, failure = c.successCount, c.failureCount
	c.mu.Unlock()
	return success, failure
}
