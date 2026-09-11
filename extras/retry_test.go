package extras

import (
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/orian/flakyhttp"
	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryBehavior(t *testing.T) {
	tests := []struct {
		name          string
		failCount     int // 0 = always fail, N = fail N times then succeed
		requestCount  int
		disableRetry  bool // if true, RetryAfter returns -1
		expectSuccess bool
		retryAfter    func(i int) time.Duration
		forceClose    bool
	}{
		{
			name:          "NoRetry_DropsMessage",
			failCount:     0, // always fail
			requestCount:  1,
			disableRetry:  true,
			expectSuccess: false,
		},
		{
			name:          "AllRetriesFail_DropsMessage",
			failCount:     0, // always fail, will exhaust all attempts (DefaultMaxAttempts)
			requestCount:  posthog.DefaultMaxAttempts,
			disableRetry:  false,
			expectSuccess: false,
		},
		{
			name:          "QuitDuringRetry_DropsMessage",
			failCount:     1, // fail, then quit forces close before retries run
			requestCount:  1,
			disableRetry:  false,
			expectSuccess: false,
			retryAfter: func(i int) time.Duration {
				return time.Minute
			},
			forceClose: true,
		},
		{
			name:          "SuccessOnLastRetry",
			failCount:     posthog.DefaultMaxAttempts - 1, // fail on every attempt but the last
			requestCount:  posthog.DefaultMaxAttempts,
			disableRetry:  false,
			expectSuccess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := flakyhttp.NewServer(flakyhttp.Config{
				Scenario:  flakyhttp.ScenarioNoResponse,
				FailCount: tc.failCount,
			})

			url, err := server.Start()
			require.NoError(t, err)
			defer server.Close()

			callback := newTestCallback(t)

			config := posthog.Config{
				Endpoint: url,
				Transport: &timeoutTransport{
					rt:      http.DefaultTransport,
					timeout: 1 * time.Second,
				},
				Interval:   1 * time.Millisecond,
				BatchSize:  1,
				Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
				Callback:   callback,
				RetryAfter: func(i int) time.Duration { return time.Millisecond },
			}

			if tc.disableRetry {
				config.MaxRetries = posthog.Ptr[int](0)
			}
			if tc.retryAfter != nil {
				config.RetryAfter = tc.retryAfter
			}

			client, err := posthog.NewWithConfig("test-api-key", config)
			require.NoError(t, err)

			err = client.Enqueue(posthog.Capture{
				DistinctId: "user1",
				Event:      "test_event",
			})
			require.NoError(t, err)

			if tc.forceClose {
				time.Sleep(10 * time.Millisecond)
				client.Close()
			}

			if tc.expectSuccess {
				// No terminal callback is asserted here. flakyhttp's success
				// response body is the fixed legacy `{"status": "ok"}`, which
				// carries no per-event results map, so the capture path cannot
				// resolve the event's outcome from it. What this case pins is
				// the retry loop: the client must keep trying until a request
				// finally reaches the server.
				//
				// Wait before closing, since Close cancels an in-progress
				// backoff and would cut the loop short.
				require.Eventually(t, func() bool {
					return server.RequestCount() >= tc.requestCount
				}, 5*time.Second, 5*time.Millisecond,
					"client should retry until a request reaches the server")

				if !tc.forceClose {
					client.Close()
				}
				assert.Equal(t, tc.requestCount, server.RequestCount())
				return
			}

			// Wait for callback
			select {
			case <-callback.successChan:
				require.Fail(t, "Expected failure but got success")
			case err := <-callback.failureChan:
				t.Logf("Event dropped as expected: %v", err)
			case <-time.After(5 * time.Second):
				require.Fail(t, "Timeout waiting for callback")
			}

			if !tc.forceClose {
				client.Close()
			}

			success, failure := callback.GetCounts()
			assert.Equal(t, tc.requestCount, server.RequestCount())
			assert.Equal(t, 1, success+failure, "Expected 1 callback")
			assert.Equal(t, 0, success, "Expected 0 success")
			assert.Equal(t, 1, failure, "Expected 1 failure")
		})
	}
}
