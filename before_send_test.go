package posthog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestBeforeSendSeesEnrichmentButNotContextOrDefaults has the hook write the
// keys it saw into options, which are sent unchanged, so the wire event shows
// what the hook saw and that a key it sets wins over the filled-in values.
func TestBeforeSendSeesEnrichmentButNotContextOrDefaults(t *testing.T) {
	exceptionList := []ExceptionItem{{Type: "t", Value: "v"}}
	tests := []struct {
		msg                Message
		wantSeenProperties []interface{}
		wantContext        bool
	}{
		{msg: Capture{Event: "e"}, wantSeenProperties: []interface{}{propertyGeoipDisable, propertyIsServer}, wantContext: true},
		{msg: Exception{ExceptionList: exceptionList}, wantSeenProperties: []interface{}{}, wantContext: true},
		{msg: Identify{DistinctId: "user-1"}},
		{msg: Alias{DistinctId: "user-1", Alias: "a"}},
		{msg: GroupIdentify{Type: "company", Key: "k"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%T", tt.msg), func(t *testing.T) {
			body, server := mockServer()
			defer server.Close()

			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:               server.URL,
				BatchSize:              1,
				now:                    mockTime,
				DefaultEventProperties: NewProperties().Set("service", "default").Set("app", "default"),
				DefaultEventOptions:    NewOptions().Set("cookieless_mode", true).Set("disable_skew_correction", true),
				BeforeSend: func(msg Message) Message {
					options := messageOptions(msg)
					options.Set("seen_options", sortedKeys(options)).Set("cookieless_mode", false)
					switch m := msg.(type) {
					case Capture:
						options.Set("seen_properties", sortedKeys(m.Properties))
						m.Properties.Set("service", "hook")
						return m
					case Exception:
						options.Set("seen_properties", sortedKeys(m.Properties))
						m.Properties = NewProperties().Merge(m.Properties).Set("service", "hook")
						return m
					}
					return msg
				},
			})
			require.NoError(t, err)
			defer client.Close()

			ctx := WithFreshRequestContext(context.Background(), RequestContext{
				Properties: NewProperties().Set("service", "context").Set("context_only", "context"),
				Options:    NewOptions().Set("product_tour_id", "context-tour"),
			})
			require.NoError(t, EnqueueWithContext(ctx, client, tt.msg))

			event := readSingleBatchEvent(t, body)
			properties := requireProperties(t, event)
			wantOptions := map[string]interface{}{
				"seen_options":            []interface{}{},
				"cookieless_mode":         false,
				"disable_skew_correction": true,
			}
			if tt.wantContext {
				wantOptions["seen_properties"] = tt.wantSeenProperties
				wantOptions["product_tour_id"] = "context-tour"
				wantOptions["process_person_profile"] = false
				require.Equal(t, "hook", properties["service"])
				require.Equal(t, "context", properties["context_only"])
			} else {
				require.Equal(t, "default", properties["service"])
			}
			require.Equal(t, wantOptions, requireOptions(t, event))
			require.Equal(t, "default", properties["app"])
		})
	}
}

func TestBeforeSendCanRemoveEnrichmentProperties(t *testing.T) {
	for _, removed := range []string{propertyIsServer, propertyGeoipDisable} {
		t.Run(removed, func(t *testing.T) {
			body, server := mockServer()
			defer server.Close()

			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				now:       mockTime,
				BeforeSend: func(msg Message) Message {
					capture := msg.(Capture)
					delete(capture.Properties, removed)
					return capture
				},
			})
			require.NoError(t, err)
			defer client.Close()

			require.NoError(t, client.Enqueue(Capture{
				DistinctId: "user-123",
				Event:      "test-event",
			}))

			properties := firstProperties(t, readBatch(t, body))
			require.NotContains(t, properties, removed)
			for _, kept := range []string{propertyIsServer, propertyGeoipDisable} {
				if kept != removed {
					require.Equal(t, true, properties[kept])
				}
			}
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

	// Person properties travel in properties.$set on the wire.
	message := firstMessage(t, readBatch(t, body))
	properties, ok := message["properties"].(map[string]interface{})
	require.True(t, ok)
	set, ok := properties["$set"].(map[string]interface{})
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

	originalGroups := Groups{"company": "posthog"}
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			capture := msg.(Capture)
			capture.Groups["company"] = "hook"
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
	originalImages := []DebugImage{{Type: "macho", DebugID: "original-debug-id"}}

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
			exception.DebugImages[0].DebugID = "hook-debug-id"
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
		DebugImages:          originalImages,
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
	require.Equal(t, "original-debug-id", originalImages[0].DebugID)
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

func TestBeforeSendCanChangeOptions(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	originalOptions := Options{
		"cookieless_mode": true,
		"nested":          map[string]interface{}{"name": "original"},
		"nested_options":  NewOptions().Set("name", "original"),
	}
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		BeforeSend: func(msg Message) Message {
			capture := msg.(Capture)
			delete(capture.Options, "cookieless_mode")
			capture.Options["nested"].(map[string]interface{})["name"] = "hook"
			capture.Options["nested_options"].(Options).Set("name", "hook")
			capture.Options.Set("product_tour_id", "tour-hook")
			// A legacy property a hook adds still moves into its option.
			capture.Properties[propertyProcessPersonProfile] = false
			return capture
		},
	})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Enqueue(Capture{
		DistinctId: "user-123",
		Event:      "test-event",
		Options:    originalOptions,
	}))

	message := firstMessage(t, readBatch(t, body))
	require.Equal(t, map[string]interface{}{
		"nested":                 map[string]interface{}{"name": "hook"},
		"nested_options":         map[string]interface{}{"name": "hook"},
		"product_tour_id":        "tour-hook",
		"process_person_profile": false,
	}, message["options"])
	require.NotContains(t, message["properties"], propertyProcessPersonProfile)
	require.Equal(t, Options{
		"cookieless_mode": true,
		"nested":          map[string]interface{}{"name": "original"},
		"nested_options":  NewOptions().Set("name", "original"),
	}, originalOptions, "the hook must not change the caller's Options")
}

func messageOptions(msg Message) Options {
	switch m := msg.(type) {
	case Capture:
		return m.Options
	case Identify:
		return m.Options
	case Alias:
		return m.Options
	case GroupIdentify:
		return m.Options
	case Exception:
		return m.Options
	}
	panic(fmt.Sprintf("unexpected message type %T", msg))
}

func TestBeforeSendIsolatesOptionsOnEveryMessageType(t *testing.T) {
	messages := []func(Options) Message{
		func(o Options) Message { return Capture{DistinctId: "d", Event: "e", Options: o} },
		func(o Options) Message { return Identify{DistinctId: "d", Options: o} },
		func(o Options) Message { return Alias{DistinctId: "d", Alias: "a", Options: o} },
		func(o Options) Message { return GroupIdentify{Type: "company", Key: "k", Options: o} },
		func(o Options) Message {
			return Exception{DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "t", Value: "v"}}, Options: o}
		},
	}
	for _, build := range messages {
		original := Options{"cookieless_mode": true}
		msg := build(original)
		t.Run(fmt.Sprintf("%T", msg), func(t *testing.T) {
			client, err := NewWithConfig("test-api-key", Config{
				Endpoint: "http://127.0.0.1:0",
				Logger:   quietTestLogger{t},
				BeforeSend: func(msg Message) Message {
					messageOptions(msg)["cookieless_mode"] = false
					return nil
				},
			})
			require.NoError(t, err)
			defer client.Close()

			require.NoError(t, client.Enqueue(msg))
			require.Equal(t, Options{"cookieless_mode": true}, original)
		})
	}
}

func TestBeforeSendGetsNonNilOptionsOnEveryMessageType(t *testing.T) {
	messages := []Message{
		Capture{DistinctId: "d", Event: "e"},
		Identify{DistinctId: "d"},
		Alias{DistinctId: "d", Alias: "a"},
		GroupIdentify{Type: "company", Key: "k"},
		Exception{DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "t", Value: "v"}}},
	}
	for _, msg := range messages {
		t.Run(fmt.Sprintf("%T", msg), func(t *testing.T) {
			body, server := mockServer()
			defer server.Close()

			client, err := NewWithConfig("test-api-key", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				Logger:    quietTestLogger{t},
				BeforeSend: func(msg Message) Message {
					messageOptions(msg).Set("future_option", "hook")
					return msg
				},
			})
			require.NoError(t, err)
			defer client.Close()

			require.NoError(t, client.Enqueue(msg))
			require.Equal(t, map[string]interface{}{"future_option": "hook"}, firstMessage(t, readBatch(t, body))["options"])
		})
	}
}

// TestPassThroughBeforeSendKeepsTheWireEventUnchanged pins that enabling a hook
// that returns its message changes nothing on the wire, on both lanes. A cloned
// nil value must stay null so a nil option still falls back to its legacy
// property, and a cloned empty slice must stay [] so it does not.
func TestPassThroughBeforeSendKeepsTheWireEventUnchanged(t *testing.T) {
	newCapture := func() Capture {
		return Capture{
			Uuid:       "8e0b2c4f-6a5d-4f1e-9c3b-2d7a1e5f9b08",
			DistinctId: "d",
			Event:      "e",
			Timestamp:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Options: Options{
				"process_person_profile":  []interface{}(nil),
				"cookieless_mode":         map[string]interface{}(nil),
				"future_option":           map[string]string(nil),
				"disable_skew_correction": []string{},
			},
			Properties: Properties{
				propertyProcessPersonProfile: false,
				propertyIgnoreSentAt:         true,
				"typed_nil_map":              map[string]interface{}(nil),
				"typed_nil_slice":            []interface{}(nil),
				"typed_nil_strings":          []string(nil),
				"empty_strings":              []string{},
				"empty_bools":                []bool{},
				"empty_ints":                 []int{},
				"empty_int64s":               []int64{},
				"empty_float64s":             []float64{},
				"$set":                       map[string]interface{}{"tags": []string{}},
			},
		}
	}
	routes := map[string]func(Client, Capture) error{
		"Enqueue":   func(c Client, m Capture) error { return c.Enqueue(m) },
		"EnqueueAI": func(c Client, m Capture) error { return c.EnqueueAI(m) },
	}
	for name, enqueue := range routes {
		t.Run(name, func(t *testing.T) {
			send := func(hook BeforeSendFunc) map[string]interface{} {
				body, server := mockServer()
				defer server.Close()
				client, err := NewWithConfig("test-api-key", Config{Endpoint: server.URL, BatchSize: 1, BeforeSend: hook})
				require.NoError(t, err)
				defer client.Close()
				require.NoError(t, enqueue(client, newCapture()))
				return firstMessage(t, readBatch(t, body))
			}

			withoutHook := send(nil)
			withHook := send(func(msg Message) Message { return msg })

			require.Equal(t, map[string]interface{}{
				"process_person_profile":  false,
				"cookieless_mode":         nil,
				"future_option":           nil,
				"disable_skew_correction": []interface{}{},
			}, withHook["options"])
			properties := withHook["properties"].(map[string]interface{})
			require.Nil(t, properties["typed_nil_strings"])
			for _, key := range []string{"empty_strings", "empty_bools", "empty_ints", "empty_int64s", "empty_float64s"} {
				require.Equal(t, []interface{}{}, properties[key], key)
			}
			require.Equal(t, map[string]interface{}{"tags": []interface{}{}}, properties["$set"])
			require.Equal(t, withoutHook["options"], withHook["options"])
			require.Equal(t, withoutHook["properties"], withHook["properties"])
		})
	}
}
