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
			failCount:     0, // always fail, will exhaust all 10 retries
			requestCount:  10,
			disableRetry:  false,
			expectSuccess: false,
		},
		{
			name:          "SuccessOnLastRetry",
			failCount:     9, // fail 9 times, succeed on 10th (last retry)
			requestCount:  10,
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
				Interval:   10 * time.Millisecond,
				BatchSize:  1,
				Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
				Callback:   callback,
				RetryAfter: func(i int) time.Duration { return time.Millisecond },
			}

			if tc.disableRetry {
				config.MaxRetries = posthog.Ptr[int](0)
			}

			client, err := posthog.NewWithConfig("test-api-key", config)
			require.NoError(t, err)

			err = client.Enqueue(posthog.Capture{
				DistinctId: "user1",
				Event:      "test_event",
			})
			require.NoError(t, err)

			// Wait for callback
			select {
			case <-callback.successChan:
				if !tc.expectSuccess {
					require.Fail(t, "Expected failure but got success")
				}
				t.Log("Event delivered successfully")
			case err := <-callback.failureChan:
				if tc.expectSuccess {
					require.Fail(t, "Expected success but got failure: %v", err)
				}
				t.Logf("Event dropped as expected: %v", err)
			case <-time.After(5 * time.Second):
				require.Fail(t, "Timeout waiting for callback")
			}

			client.Close()

			success, failure := callback.GetCounts()
			assert.Equal(t, tc.requestCount, server.RequestCount())
			assert.Equal(t, 1, success+failure, "Expected 1 callback")

			if tc.expectSuccess {
				assert.Equal(t, 1, success, "Expected 1 success")
				assert.Equal(t, 0, failure, "Expected 0 failures")
			} else {
				assert.Equal(t, 0, success, "Expected 0 success")
				assert.Equal(t, 1, failure, "Expected 1 failure")
			}
		})
	}
}
