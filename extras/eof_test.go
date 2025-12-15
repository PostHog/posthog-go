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

// TestEOFScenarios test what happens when a response is not fully received.
func TestEOFScenarios(t *testing.T) {
	tests := []struct {
		name            string
		scenario        flakyhttp.Scenario
		partialBodySize int
		disableRetries  bool
		expectFailure   bool
	}{
		{
			name:            "HeadersOnlyClose_PartialBody",
			scenario:        flakyhttp.ScenarioHeadersOnlyClose,
			partialBodySize: 10,
			disableRetries:  true,
			expectFailure:   false, // may succeed or fail depending on timing
		},
		{
			name:           "HeadersOnlyClose_NoBody",
			scenario:       flakyhttp.ScenarioHeadersOnlyClose,
			disableRetries: true,
			expectFailure:  false, // may succeed or fail
		},
		{
			name:           "NoResponse",
			scenario:       flakyhttp.ScenarioNoResponse,
			disableRetries: true,
			expectFailure:  true, // must fail - server sends nothing
		},
		{
			name:           "StatusOnly",
			scenario:       flakyhttp.ScenarioStatusOnly,
			disableRetries: true,
			expectFailure:  true, // must fail - incomplete response
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := flakyhttp.Config{
				Scenario:        tc.scenario,
				PartialBodySize: tc.partialBodySize,
			}
			server := flakyhttp.NewServer(config)

			url, err := server.Start()
			require.NoError(t, err, "Failed to start mock server")
			defer server.Close()
			t.Logf("Mock server started at %s", url)

			callback := newTestCallback(t)

			clientConfig := posthog.Config{
				Endpoint: url,
				Transport: &timeoutTransport{
					rt:      http.DefaultTransport,
					timeout: 1 * time.Second,
				},
				Interval:   50 * time.Millisecond,
				BatchSize:  1,
				Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
				Callback:   callback,
				RetryAfter: func(i int) time.Duration { return time.Millisecond },
			}
			if tc.disableRetries {
				clientConfig.MaxRetries = posthog.Ptr[int](0)
			}

			client, err := posthog.NewWithConfig("test-api-key", clientConfig)
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

			// Wait for callback
			select {
			case capturedErr := <-callback.failureChan:
				t.Logf("Received error callback: %v", capturedErr)
			case <-callback.successChan:
				if tc.expectFailure {
					require.Fail(t, "Expected failure but got success")
				}
				t.Log("Received success callback")
			case <-time.After(2 * time.Second):
				require.Fail(t, "Timeout waiting for callback")
			}

			client.Close()

			success, failure := callback.GetCounts()

			assert.Equal(t, 1, server.RequestCount(), "Expected at least 1 request to the server")
			assert.Equal(t, 1, success+failure, "Expected at least 1 callback")

			if tc.expectFailure {
				assert.Equal(t, 1, failure, "Expected failure callback")
			} else {
				assert.Equal(t, 1, success, "Expected success callback")
			}
		})
	}
}
