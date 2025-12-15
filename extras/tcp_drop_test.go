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

// TestTCPDropRecovery tests scenarios where server fails initially but succeeds after retries.
// Failures are simulated at the TCP connection level, during receiving the request.
func TestTCPDropRecovery(t *testing.T) {
	tests := []struct {
		name        string
		scenario    flakyhttp.TCPScenario
		bytesToRead int
	}{
		{
			name:     "DropAfterConnect",
			scenario: flakyhttp.TCPScenarioDropAfterConnect,
		},
		{
			name:     "DropAfterHeaders",
			scenario: flakyhttp.TCPScenarioDropAfterHeaders,
		},
		{
			name:     "DropDuringBody",
			scenario: flakyhttp.TCPScenarioDropDuringBody,
		},
		{
			name:        "DropAfterPartialBody",
			scenario:    flakyhttp.TCPScenarioDropAfterPartialBody,
			bytesToRead: 50,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			firstFailures := 3
			server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
				Scenario:    tc.scenario,
				BytesToRead: tc.bytesToRead,
				FailCount:   firstFailures, // Fail 3 times, then succeed
				SendRST:     true,
			})

			url, err := server.Start()
			require.NoError(t, err, "Failed to start server")
			defer server.Close()
			t.Logf("TCP server started at %s", url)

			callback := newTestCallback(t)

			client, err := posthog.NewWithConfig(
				"test-api-key",
				posthog.Config{
					Endpoint: url,
					Transport: &timeoutTransport{
						rt:      http.DefaultTransport,
						timeout: 1 * time.Second,
					},
					RetryAfter: func(i int) time.Duration { return time.Millisecond },
					Interval:   10 * time.Millisecond,
					BatchSize:  1,
					Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
					Callback:   callback,
				},
			)
			require.NoError(t, err, "Failed to create client")

			err = client.Enqueue(posthog.Capture{
				DistinctId: "user1",
				Event:      "tcp_drop_recovery_test",
			})
			require.NoError(t, err, "Failed to enqueue")

			// Wait for success (after retries)
			select {
			case <-callback.successChan:
				t.Log("Event delivered successfully after retries")
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
			assert.Equal(t, 0, failure, "Expected 0 failures")
			assert.Equal(t, 1, server.SuccessCount())
			assert.Equal(t, firstFailures+1, server.ConnCount())
		})
	}
}

// TestTCPDropFailure tests scenarios where all requests fail (server always drops).
func TestTCPDropFailure(t *testing.T) {
	tests := []struct {
		name        string
		scenario    flakyhttp.TCPScenario
		bytesToRead int
		sendRST     bool // true = RST, false = graceful FIN
	}{
		{
			name:     "DropAfterConnect_RST",
			scenario: flakyhttp.TCPScenarioDropAfterConnect,
			sendRST:  true,
		},
		{
			name:     "DropAfterHeaders_RST",
			scenario: flakyhttp.TCPScenarioDropAfterHeaders,
			sendRST:  true,
		},
		{
			name:     "DropDuringBody_RST",
			scenario: flakyhttp.TCPScenarioDropDuringBody,
			sendRST:  true,
		},
		{
			name:        "DropAfterPartialBody_RST",
			scenario:    flakyhttp.TCPScenarioDropAfterPartialBody,
			bytesToRead: 50,
			sendRST:     true,
		},
		{
			name:     "DropAfterHeaders_GracefulClose",
			scenario: flakyhttp.TCPScenarioDropAfterHeaders,
			sendRST:  false, // Graceful close (FIN)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := flakyhttp.NewTCPServer(flakyhttp.TCPServerConfig{
				Scenario:    tc.scenario,
				BytesToRead: tc.bytesToRead,
				FailCount:   0, // Always fail
				SendRST:     tc.sendRST,
			})

			url, err := server.Start()
			require.NoError(t, err, "Failed to start server")
			defer server.Close()
			t.Logf("TCP server started at %s", url)

			callback := newTestCallback(t)

			client, err := posthog.NewWithConfig(
				"test-api-key",
				posthog.Config{
					Endpoint: url,
					Transport: &timeoutTransport{
						rt:      http.DefaultTransport,
						timeout: 5 * time.Second,
					},
					Interval:   50 * time.Millisecond,
					BatchSize:  1,
					RetryAfter: func(i int) time.Duration { return time.Millisecond },
					Logger:     posthog.StdLogger(log.New(os.Stderr, "[posthog] ", log.LstdFlags), true),
					Callback:   callback,
				},
			)
			require.NoError(t, err, "Failed to create client")

			err = client.Enqueue(posthog.Capture{
				DistinctId: "user1",
				Event:      "tcp_drop_failure_test",
			})
			require.NoError(t, err, "Failed to enqueue")

			// Wait for failure (after max retries)
			select {
			case err := <-callback.failureChan:
				t.Logf("Event failed as expected: %v", err)
			case <-callback.successChan:
				require.Fail(t, "Unexpected success - server should always fail")
			case <-time.After(10 * time.Second):
				require.Fail(t, "Timeout waiting for callback")
			}

			client.Close()

			success, failure := callback.GetCounts()
			t.Logf("Results: %d success, %d failure", success, failure)
			t.Logf("Server received %d connections", server.ConnCount())

			assert.Equal(t, 1, failure, "Expected 1 failure")
			assert.Equal(t, 0, success, "Expected 0 success")
		})
	}
}
