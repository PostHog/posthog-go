package posthog

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestBeforeSendCaptureHook(t *testing.T) {
	tests := []struct {
		name          string
		capture       Capture
		beforeSend    BeforeSendFunc
		expectRequest bool
		expectLog     string
		assert        func(*testing.T, map[string]interface{}, map[string]interface{})
	}{
		{
			name: "modifies capture after enrichment",
			beforeSend: func(msg Message) Message {
				capture := msg.(Capture)
				capture.Properties["type"] = capture.Type
				capture.Properties["timestamp"] = capture.Timestamp
				capture.Properties["added_by_hook"] = true
				return capture
			},
			expectRequest: true,
			assert: func(t *testing.T, _ map[string]interface{}, properties map[string]interface{}) {
				require.Equal(t, "capture", properties["type"])
				require.Equal(t, mockTime().Format(time.RFC3339), properties["timestamp"])
				require.Equal(t, true, properties["added_by_hook"])
			},
		},
		{
			name: "nil drops message",
			beforeSend: func(Message) Message {
				return nil
			},
			expectLog: "BeforeSend returned nil for posthog.Capture; dropping message",
		},
		{
			name: "panic drops message",
			beforeSend: func(msg Message) Message {
				capture := msg.(Capture)
				capture.Properties["panic_leaked"] = true
				panic("boom")
			},
			expectLog: "panic in BeforeSend hook for posthog.Capture: boom; dropping message",
		},
		{
			name: "invalid return drops message",
			beforeSend: func(msg Message) Message {
				capture := msg.(Capture)
				capture.Properties["invalid_leaked"] = true
				capture.Event = ""
				return capture
			},
			expectLog: "BeforeSend returned invalid posthog.Capture",
		},
		{
			name: "type change drops message",
			beforeSend: func(Message) Message {
				return Identify{DistinctId: "user-123"}
			},
			expectLog: "BeforeSend returned posthog.Identify instead of posthog.Capture; dropping message",
		},
		{
			name: "receives expanded feature flag properties",
			capture: Capture{
				Properties:       NewProperties(),
				SendFeatureFlags: SendFeatureFlagsWithOptions(&SendFeatureFlagsOptions{OnlyEvaluateLocally: true}),
				Flags:            &FeatureFlagEvaluations{flags: map[string]evaluatedFlagRecord{"flag-a": {Key: "flag-a", Enabled: true}}},
			},
			beforeSend: func(msg Message) Message {
				capture := msg.(Capture)
				capture.Properties["flags_nil"] = capture.Flags == nil
				capture.Properties["send_feature_flags_nil"] = capture.SendFeatureFlags == nil
				capture.Properties["hook_ran"] = true
				return capture
			},
			expectRequest: true,
			assert: func(t *testing.T, _ map[string]interface{}, properties map[string]interface{}) {
				require.Equal(t, true, properties["$feature/flag-a"])
				require.Equal(t, true, properties["flags_nil"])
				require.Equal(t, true, properties["send_feature_flags_nil"])
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

			var (
				logged   []string
				loggedMu sync.Mutex
			)
			appendLog := func(format string, args ...interface{}) {
				loggedMu.Lock()
				logged = append(logged, formatMessage(format, args...))
				loggedMu.Unlock()
			}
			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:   server.URL,
				BatchSize:  1,
				now:        mockTime,
				BeforeSend: tt.beforeSend,
				Logger: testLogger{
					logf:   appendLog,
					errorf: appendLog,
				},
			})
			require.NoError(t, err)

			capture := tt.capture
			if capture.DistinctId == "" {
				capture.DistinctId = "user-123"
			}
			if capture.Event == "" {
				capture.Event = "test-event"
			}
			require.NoError(t, client.Enqueue(capture))
			require.NoError(t, client.Close())

			if tt.expectLog != "" {
				loggedMu.Lock()
				logs := append([]string(nil), logged...)
				loggedMu.Unlock()
				require.True(t, containsLog(logs, tt.expectLog), "logs: %v", logs)
			}
			if !tt.expectRequest {
				require.Zero(t, requests.Load())
				return
			}

			require.Equal(t, int64(1), requests.Load())
			batch := readBatch(t, body)
			message := firstMessage(t, batch)
			properties := firstProperties(t, batch)
			tt.assert(t, message, properties)
		})
	}
}

func TestBeforeSendDoesNotMutateOriginalProperties(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	originalProperties := Properties{
		"email": "test@example.com",
		"nested": map[string]interface{}{
			"name": "original",
			"items": []interface{}{
				map[string]interface{}{"value": "first"},
			},
		},
		"tags": []string{"one", "two"},
	}
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			identify := msg.(Identify)
			identify.Properties["hook_ran"] = true
			identify.Properties["nested"].(map[string]interface{})["name"] = "hook"
			identify.Properties["nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"] = "hook"
			identify.Properties["tags"].([]string)[0] = "hook"
			return identify
		},
	})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Enqueue(Identify{
		DistinctId: "user-123",
		Properties: originalProperties,
	}))

	message := firstMessage(t, readBatch(t, body))
	set, ok := message["$set"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, true, set["hook_ran"])
	require.Nil(t, originalProperties["hook_ran"])
	require.Equal(t, "original", originalProperties["nested"].(map[string]interface{})["name"])
	require.Equal(t, "first", originalProperties["nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"])
	require.Equal(t, []string{"one", "two"}, originalProperties["tags"])
}

func TestBeforeSendDoesNotMutateOriginalGroups(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	originalGroups := Groups{
		"company": "posthog",
		"nested": map[string]interface{}{
			"name": "original",
		},
	}
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			capture := msg.(Capture)
			capture.Groups["company"] = "hook"
			capture.Groups["nested"].(map[string]interface{})["name"] = "hook"
			return capture
		},
	})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Enqueue(Capture{
		DistinctId: "user-123",
		Event:      "test-event",
		Groups:     originalGroups,
	}))

	properties := firstProperties(t, readBatch(t, body))
	groups, ok := properties["$groups"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "hook", groups["company"])
	require.Equal(t, "posthog", originalGroups["company"])
	require.Equal(t, "original", originalGroups["nested"].(map[string]interface{})["name"])
}

func TestBeforeSendDoesNotMutateOriginalExceptionData(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	handled := true
	synthetic := false
	fingerprint := "original-fingerprint"
	originalList := []ExceptionItem{{
		Type:  "RuntimeError",
		Value: "boom",
		Mechanism: &ExceptionMechanism{
			Handled:   &handled,
			Synthetic: &synthetic,
		},
		Stacktrace: &ExceptionStacktrace{
			Type: "raw",
			Frames: []StackFrame{{
				Filename: "original.go",
				LineNo:   1,
			}},
		},
	}}

	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			exception := msg.(Exception)
			if exception.Properties == nil {
				exception.Properties = NewProperties()
			}
			*exception.ExceptionFingerprint = "hook-fingerprint"
			exception.ExceptionList[0].Type = "HookError"
			exception.ExceptionList[0].Value = "changed"
			*exception.ExceptionList[0].Mechanism.Handled = false
			*exception.ExceptionList[0].Mechanism.Synthetic = true
			exception.ExceptionList[0].Stacktrace.Type = "hook"
			exception.ExceptionList[0].Stacktrace.Frames[0].Filename = "hook.go"
			exception.ExceptionList[0].Stacktrace.Frames[0].LineNo = 2
			exception.Properties["hook_ran"] = true
			return exception
		},
	})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Enqueue(Exception{
		DistinctId:           "user-123",
		Properties:           NewProperties(),
		ExceptionList:        originalList,
		ExceptionFingerprint: &fingerprint,
	}))

	message := firstMessage(t, readBatch(t, body))
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, true, properties["hook_ran"])
	require.Equal(t, "original-fingerprint", fingerprint)
	require.Equal(t, "RuntimeError", originalList[0].Type)
	require.Equal(t, "boom", originalList[0].Value)
	require.True(t, *originalList[0].Mechanism.Handled)
	require.False(t, *originalList[0].Mechanism.Synthetic)
	require.Equal(t, "raw", originalList[0].Stacktrace.Type)
	require.Equal(t, "original.go", originalList[0].Stacktrace.Frames[0].Filename)
	require.Equal(t, 1, originalList[0].Stacktrace.Frames[0].LineNo)
}

func TestBeforeSendReceivesTypedMessagesBeforeAPIfy(t *testing.T) {
	tests := []struct {
		name       string
		msg        Message
		beforeSend BeforeSendFunc
		assert     func(*testing.T, map[string]interface{})
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
			beforeSend: func(msg Message) Message {
				exception := msg.(Exception)
				if len(exception.ExceptionList) != 1 {
					return Exception{}
				}
				if exception.Properties == nil {
					exception.Properties = NewProperties()
				}
				exception.Properties["seen_exception"] = true
				return exception
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

			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:   server.URL,
				BatchSize:  1,
				now:        mockTime,
				BeforeSend: tt.beforeSend,
			})
			require.NoError(t, err)
			defer client.Close()

			require.NoError(t, client.Enqueue(tt.msg))

			message := firstMessage(t, readBatch(t, body))
			tt.assert(t, message)
		})
	}
}

func formatMessage(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

func containsLog(logs []string, want string) bool {
	for _, log := range logs {
		if strings.Contains(log, want) {
			return true
		}
	}
	return false
}
