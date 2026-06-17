package posthog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func readBatch(t *testing.T, body <-chan []byte) map[string]interface{} {
	t.Helper()

	select {
	case payload := <-body:
		var batch map[string]interface{}
		require.NoError(t, json.Unmarshal(payload, &batch))
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for batch request")
		return nil
	}
}

func firstMessage(t *testing.T, batch map[string]interface{}) map[string]interface{} {
	t.Helper()

	messages, ok := batch["batch"].([]interface{})
	require.True(t, ok)
	require.Len(t, messages, 1)

	message, ok := messages[0].(map[string]interface{})
	require.True(t, ok)
	return message
}

func firstProperties(t *testing.T, batch map[string]interface{}) map[string]interface{} {
	t.Helper()

	message := firstMessage(t, batch)
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	return properties
}

func TestBeforeSendCaptureHooks(t *testing.T) {
	tests := []struct {
		name             string
		capture          Capture
		configBeforeSend func(*testing.T) BeforeSendFunc
		registerHooks    func(*testing.T, Client, *atomic.Int64)
		expectRequest    bool
		assert           func(*testing.T, map[string]interface{}, map[string]interface{}, *atomic.Int64)
	}{
		{
			name: "runs config and client hooks in order",
			configBeforeSend: func(t *testing.T) BeforeSendFunc {
				return func(msg Message) Message {
					capture, ok := msg.(Capture)
					require.True(t, ok)
					require.Equal(t, "capture", capture.Type)
					require.Equal(t, mockTime(), capture.Timestamp)
					capture.Properties["order"] = "config"
					return capture
				}
			},
			registerHooks: func(t *testing.T, client Client, _ *atomic.Int64) {
				client.BeforeSend(func(msg Message) Message {
					capture, ok := msg.(Capture)
					require.True(t, ok)
					capture.Properties["order"] = capture.Properties["order"].(string) + ",client"
					capture.Properties["added_by_hook"] = true
					return capture
				})
			},
			expectRequest: true,
			assert: func(t *testing.T, _ map[string]interface{}, properties map[string]interface{}, _ *atomic.Int64) {
				require.Equal(t, "config,client", properties["order"])
				require.Equal(t, true, properties["added_by_hook"])
			},
		},
		{
			name: "nil drops message and stops later hooks",
			registerHooks: func(_ *testing.T, client Client, secondHookCalls *atomic.Int64) {
				client.BeforeSend(func(Message) Message { return nil })
				client.BeforeSend(func(msg Message) Message {
					secondHookCalls.Add(1)
					return msg
				})
			},
			expectRequest: false,
			assert: func(t *testing.T, _ map[string]interface{}, _ map[string]interface{}, secondHookCalls *atomic.Int64) {
				require.Zero(t, secondHookCalls.Load())
			},
		},
		{
			name: "panic keeps current message without leaking mutations",
			registerHooks: func(_ *testing.T, client Client, _ *atomic.Int64) {
				client.BeforeSend(func(msg Message) Message {
					capture := msg.(Capture)
					capture.Properties["panic_leaked"] = true
					panic("boom")
				})
				client.BeforeSend(func(msg Message) Message {
					capture := msg.(Capture)
					capture.Properties["after_error"] = true
					return capture
				})
			},
			expectRequest: true,
			assert: func(t *testing.T, message map[string]interface{}, properties map[string]interface{}, _ *atomic.Int64) {
				require.Equal(t, "user-123", message["distinct_id"])
				require.Equal(t, true, properties["after_error"])
				require.Nil(t, properties["panic_leaked"])
			},
		},
		{
			name: "invalid return keeps current message without leaking mutations",
			registerHooks: func(_ *testing.T, client Client, _ *atomic.Int64) {
				client.BeforeSend(func(msg Message) Message {
					capture := msg.(Capture)
					capture.Properties["invalid_leaked"] = true
					capture.Event = ""
					return capture
				})
				client.BeforeSend(func(msg Message) Message {
					capture := msg.(Capture)
					capture.Properties["after_error"] = true
					return capture
				})
			},
			expectRequest: true,
			assert: func(t *testing.T, message map[string]interface{}, properties map[string]interface{}, _ *atomic.Int64) {
				require.Equal(t, "user-123", message["distinct_id"])
				require.Equal(t, true, properties["after_error"])
				require.Nil(t, properties["invalid_leaked"])
			},
		},
		{
			name: "does not expose processed feature flag inputs",
			capture: Capture{
				Properties:       NewProperties(),
				SendFeatureFlags: SendFeatureFlagsWithOptions(&SendFeatureFlagsOptions{OnlyEvaluateLocally: true}),
				Flags:            &FeatureFlagEvaluations{flags: map[string]evaluatedFlagRecord{"flag-a": {Key: "flag-a", Enabled: true}}},
			},
			registerHooks: func(t *testing.T, client Client, _ *atomic.Int64) {
				client.BeforeSend(func(msg Message) Message {
					capture, ok := msg.(Capture)
					require.True(t, ok)
					require.Nil(t, capture.Flags)
					require.Nil(t, capture.SendFeatureFlags)
					require.Equal(t, true, capture.Properties["$feature/flag-a"])
					capture.Properties["hook_ran"] = true
					return capture
				})
			},
			expectRequest: true,
			assert: func(t *testing.T, _ map[string]interface{}, properties map[string]interface{}, _ *atomic.Int64) {
				require.Equal(t, true, properties["$feature/flag-a"])
				require.Equal(t, true, properties["hook_ran"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := make(chan []byte, 1)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				payload, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				body <- payload
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			config := Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				now:       mockTime,
			}
			if tt.configBeforeSend != nil {
				config.BeforeSend = tt.configBeforeSend(t)
			}
			client, err := NewWithConfig("test-api-key", config)
			require.NoError(t, err)

			var secondHookCalls atomic.Int64
			if tt.registerHooks != nil {
				tt.registerHooks(t, client, &secondHookCalls)
			}

			capture := tt.capture
			if capture.DistinctId == "" {
				capture.DistinctId = "user-123"
			}
			if capture.Event == "" {
				capture.Event = "test-event"
			}
			require.NoError(t, client.Enqueue(capture))
			require.NoError(t, client.Close())

			if !tt.expectRequest {
				require.Zero(t, requests.Load())
				tt.assert(t, nil, nil, &secondHookCalls)
				return
			}

			require.Equal(t, int64(1), requests.Load())
			batch := readBatch(t, body)
			message := firstMessage(t, batch)
			properties := firstProperties(t, batch)
			tt.assert(t, message, properties, &secondHookCalls)
		})
	}
}

func TestBeforeSendReceivesTypedMessagesBeforeAPIfy(t *testing.T) {
	tests := []struct {
		name   string
		msg    Message
		assert func(*testing.T, map[string]interface{})
	}{
		{
			name: "exception",
			msg: Exception{
				DistinctId: "user-123",
				Properties: NewProperties(),
				ExceptionList: []ExceptionItem{{
					Type:  "error type",
					Value: "error value",
				}},
			},
			assert: func(t *testing.T, message map[string]interface{}) {
				properties, ok := message["properties"].(map[string]interface{})
				require.True(t, ok)
				require.Equal(t, "$exception", message["event"])
				require.Equal(t, true, properties["seen_exception"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, server := mockServer()
			defer server.Close()

			var sawMessage atomic.Bool
			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				now:       mockTime,
			})
			require.NoError(t, err)
			defer client.Close()

			client.BeforeSend(func(msg Message) Message {
				exception, ok := msg.(Exception)
				require.True(t, ok)
				require.Len(t, exception.ExceptionList, 1)
				sawMessage.Store(true)
				if exception.Properties == nil {
					exception.Properties = NewProperties()
				}
				exception.Properties["seen_exception"] = true
				return exception
			})

			require.NoError(t, client.Enqueue(tt.msg))

			message := firstMessage(t, readBatch(t, body))
			require.True(t, sawMessage.Load())
			tt.assert(t, message)
		})
	}
}
