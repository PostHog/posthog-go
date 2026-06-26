package posthog

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestMakeFlagsRequestRetriesTransientTransportErrorThenSucceeds(t *testing.T) {
	var calls atomic.Int32

	client, err := newFlagsClient("test-api-key", "http://posthog.test", http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading request body: %v", err)
			}
			if !strings.Contains(string(body), `"distinct_id":"user-1"`) {
				t.Fatalf("request body was not rebuilt correctly: %s", string(body))
			}

			if calls.Load() == 1 {
				return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"featureFlags":{"beta-feature":true},"featureFlagPayloads":{}}`)),
				Request:    r,
			}, nil
		}),
	}, time.Second, testLogger{t.Logf, t.Logf}, nil)
	if err != nil {
		t.Fatalf("newFlagsClient returned error: %v", err)
	}
	client.retryAfter = func(int) time.Duration { return 0 }

	res, err := client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("makeFlagsRequest returned error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
	if got := res.FeatureFlags["beta-feature"]; got != true {
		t.Fatalf("expected beta-feature=true, got %#v", got)
	}
}

func TestMakeFlagsRequestRetriesReadNetworkErrorThenSucceeds(t *testing.T) {
	var calls atomic.Int32

	client, err := newFlagsClient("test-api-key", "http://posthog.test", http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if calls.Load() == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       failingReadCloser{},
					Request:    r,
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"featureFlags":{"beta-feature":true},"featureFlagPayloads":{}}`)),
				Request:    r,
			}, nil
		}),
	}, time.Second, testLogger{t.Logf, t.Logf}, nil)
	if err != nil {
		t.Fatalf("newFlagsClient returned error: %v", err)
	}
	client.retryAfter = func(int) time.Duration { return 0 }

	res, err := client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("makeFlagsRequest returned error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
	if got := res.FeatureFlags["beta-feature"]; got != true {
		t.Fatalf("expected beta-feature=true, got %#v", got)
	}
}

func TestMakeFlagsRequestDoesNotRetryWhenConfiguredMaxRetriesIsZero(t *testing.T) {
	var calls atomic.Int32

	client, err := newFlagsClient("test-api-key", "http://posthog.test", http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}),
	}, time.Second, testLogger{t.Logf, t.Logf}, Ptr(0))
	if err != nil {
		t.Fatalf("newFlagsClient returned error: %v", err)
	}
	client.retryAfter = func(int) time.Duration { return 0 }

	_, err = client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected no retries when configured max retries is 0; got %d calls", got)
	}
}

func TestMakeFlagsRequestDoesNotRetryConnectionRefused(t *testing.T) {
	var calls atomic.Int32

	client, err := newFlagsClient("test-api-key", "http://posthog.test", http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}),
	}, time.Second, testLogger{t.Logf, t.Logf}, nil)
	if err != nil {
		t.Fatalf("newFlagsClient returned error: %v", err)
	}
	client.retryAfter = func(int) time.Duration { return 0 }

	_, err = client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected no retries for connection refused; got %d calls", got)
	}
}

func TestMakeFlagsRequestDoesNotRetryHTTPStatusErrors(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()

			client, err := newFlagsClient("test-api-key", server.URL, http.Client{}, time.Second, testLogger{t.Logf, t.Logf}, nil)
			if err != nil {
				t.Fatalf("newFlagsClient returned error: %v", err)
			}
			client.retryAfter = func(int) time.Duration { return 0 }

			_, err = client.makeFlagsRequest("user-1", nil, nil, nil, nil, false, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected APIError, got %T: %v", err, err)
			}
			if apiErr.StatusCode != status {
				t.Fatalf("expected status %d, got %d", status, apiErr.StatusCode)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("expected no retries for HTTP status %d; got %d calls", status, got)
			}
		})
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (failingReadCloser) Close() error { return nil }
