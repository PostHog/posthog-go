package posthogmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEnqueueClient struct {
	messages []posthog.Message
	errors   []error
}

func (f *fakeEnqueueClient) Enqueue(message posthog.Message) error {
	f.messages = append(f.messages, message)
	index := len(f.messages) - 1
	if index < len(f.errors) {
		return f.errors[index]
	}
	return nil
}

func TestCaptureToolCallMinimal(t *testing.T) {
	client := &fakeEnqueueClient{}
	analytics := New(client)

	require.NoError(t, analytics.CaptureToolCall(context.Background(), ToolCall{ToolName: "search_docs"}))
	require.Len(t, client.messages, 1)

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "anonymous", capture.DistinctId)
	assert.Equal(t, eventToolCall, capture.Event)
	assert.Equal(t, analyticsSource, capture.Properties[propertySource])
	assert.Equal(t, "search_docs", capture.Properties[propertyResourceName])
	assert.Equal(t, "search_docs", capture.Properties[propertyToolName])
	assert.Equal(t, float64(0), capture.Properties[propertyDurationMS])
	assert.Equal(t, false, capture.Properties[propertyIsError])
	assert.Equal(t, false, capture.Properties[propertyProcessProfile])
	assert.NotContains(t, capture.Properties, propertySessionID)
	assert.NotContains(t, capture.Properties, propertySet)
	assert.Nil(t, capture.Groups)
}

func TestCaptureToolCallCompleteMappingAndPrecedence(t *testing.T) {
	client := &fakeEnqueueClient{}
	analytics := New(client)
	parameters := map[string]any{"query": "select 1"}
	response := map[string]any{"rows": []any{1, 2}}
	groups := posthog.Groups{"organization": "org_1"}
	setProperties := posthog.Properties{"plan": "pro"}
	custom := posthog.Properties{
		propertyClientName:     "custom-client",
		propertyGroups:         map[string]any{"organization": "wrong"},
		propertySet:            map[string]any{"plan": "wrong"},
		propertyProcessProfile: false,
		propertySessionID:      "wrong-session",
		"environment":          "test",
	}

	require.NoError(t, analytics.CaptureToolCall(context.Background(), ToolCall{
		ToolName:        "query",
		ToolDescription: "Run a query",
		ToolCategory:    "Data",
		DistinctID:      "user_1",
		SessionID:       "session_1",
		Groups:          groups,
		SetProperties:   setProperties,
		ServerName:      "server",
		ServerVersion:   "1.0",
		ClientName:      "client",
		ClientVersion:   "2.0",
		ProtocolVersion: "2026-07-28",
		Intent:          "  inspect data  ",
		Parameters:      parameters,
		Response:        response,
		Duration:        1500 * time.Microsecond,
		Properties:      custom,
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "user_1", capture.DistinctId)
	assert.Equal(t, float64(1.5), capture.Properties[propertyDurationMS])
	assert.Equal(t, "client", capture.Properties[propertyClientName])
	assert.Equal(t, "session_1", capture.Properties[propertySessionID])
	assert.Equal(t, "inspect data", capture.Properties[propertyIntent])
	assert.Equal(t, string(IntentSourceContextParameter), capture.Properties[propertyIntentSource])
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, capture.Groups)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, capture.Properties[propertyGroups])
	assert.Equal(t, posthog.Properties{"plan": "pro"}, capture.Properties[propertySet])
	assert.Equal(t, false, capture.Properties[propertyProcessProfile])
	assert.Equal(t, "test", capture.Properties["environment"])

	assert.Equal(t, map[string]any{"query": "select 1"}, parameters)
	assert.Equal(t, map[string]any{"rows": []any{1, 2}}, response)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, groups)
	assert.Equal(t, posthog.Properties{"plan": "pro"}, setProperties)
	assert.Equal(t, false, custom[propertyProcessProfile])
}

func TestCaptureToolCallPersonProfileSerializedPayloads(t *testing.T) {
	for _, test := range []struct {
		name       string
		distinctID string
		flag       any
		want       any
	}{
		{
			name:       "identified explicit opt-out",
			distinctID: "user_1",
			flag:       false,
			want:       false,
		},
		{
			name:       "identified omits explicit true",
			distinctID: "user_1",
			flag:       true,
			want:       nil,
		},
		{
			name: "anonymous cannot opt in",
			flag: true,
			want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
				ToolName:   "query",
				DistinctID: test.distinctID,
				IsError:    true,
				Error:      errors.New("request failed"),
				Properties: posthog.Properties{propertyProcessProfile: test.flag},
			}))
			require.Len(t, client.messages, 2)

			assertSerializedProperty(t, client.messages[0], propertyProcessProfile, test.want)
			assertSerializedProperty(t, client.messages[1], propertyProcessProfile, test.want)
		})
	}
}

func TestCaptureToolCallPersonProfileOptOutSurvivesClientDefaults(t *testing.T) {
	payloads := make(chan []byte, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read capture request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		payloads <- body
	}))
	defer server.Close()

	client, err := posthog.NewWithConfig("test-key", posthog.Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		DefaultEventProperties: posthog.Properties{
			propertyProcessProfile: true,
			"service":              "api",
		},
	})
	require.NoError(t, err)
	defer client.Close()

	for _, test := range []struct {
		name string
		call ToolCall
		want bool
	}{
		{name: "identified opt-out", call: ToolCall{ToolName: "query", DistinctID: "user_1", Properties: posthog.Properties{propertyProcessProfile: false}}, want: false},
		{name: "anonymous opt-out", call: ToolCall{ToolName: "query"}, want: false},
		{name: "identified default", call: ToolCall{ToolName: "query", DistinctID: "user_2"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, New(client).CaptureToolCall(context.Background(), test.call))
			select {
			case body := <-payloads:
				var payload struct {
					Batch []struct {
						Properties map[string]any `json:"properties"`
					} `json:"batch"`
				}
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Len(t, payload.Batch, 1)
				properties := payload.Batch[0].Properties
				assert.Equal(t, test.want, properties[propertyProcessProfile])
				assert.Equal(t, "api", properties["service"])
			case <-time.After(5 * time.Second):
				t.Fatal("timeout waiting for capture request")
			}
		})
	}
}

func TestCaptureToolCallSessionFallbackAndAnonymousSetSuppression(t *testing.T) {
	for _, test := range []struct {
		name       string
		call       ToolCall
		distinctID string
		personless bool
	}{
		{
			name:       "session fallback",
			call:       ToolCall{ToolName: "tool", SessionID: "session_1", SetProperties: posthog.Properties{"email": "ignored"}},
			distinctID: "session_1",
			personless: true,
		},
		{
			name:       "anonymous fallback",
			call:       ToolCall{ToolName: "tool", SetProperties: posthog.Properties{"email": "ignored"}},
			distinctID: "anonymous",
			personless: true,
		},
		{
			name:       "explicit identity",
			call:       ToolCall{ToolName: "tool", DistinctID: "user_1"},
			distinctID: "user_1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(context.Background(), test.call))
			capture := requireCapture(t, client.messages[0])
			assert.Equal(t, test.distinctID, capture.DistinctId)
			assert.NotContains(t, capture.Properties, propertySet)
			if test.personless {
				assert.Equal(t, false, capture.Properties[propertyProcessProfile])
			} else {
				assert.NotContains(t, capture.Properties, propertyProcessProfile)
			}
		})
	}
}

func TestCaptureToolCallValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		call ToolCall
		want string
	}{
		{name: "empty name", call: ToolCall{}, want: "ToolName"},
		{name: "blank name", call: ToolCall{ToolName: "  \t"}, want: "ToolName"},
		{name: "negative duration", call: ToolCall{ToolName: "tool", Duration: -1}, want: "Duration"},
		{name: "invalid intent source", call: ToolCall{ToolName: "tool", IntentSource: "model"}, want: "IntentSource"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			err := New(client).CaptureToolCall(context.Background(), test.call)
			require.ErrorContains(t, err, test.want)
			assert.Empty(t, client.messages)
		})
	}

	require.ErrorContains(t, (*Analytics)(nil).CaptureToolCall(context.Background(), ToolCall{ToolName: "tool"}), "nil enqueue client")
	require.ErrorContains(t, New(nil).CaptureToolCall(context.Background(), ToolCall{ToolName: "tool"}), "nil enqueue client")
}

type panickingError struct{}

func (panickingError) Error() string { panic("should not run") }

func TestCaptureToolCallFailureAndException(t *testing.T) {
	client := &fakeEnqueueClient{}
	token := "phc_abcdefghijklmnopqrstuvwxyz"
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName:   "query",
		DistinctID: "user_1",
		Groups:     posthog.Groups{"organization": "org_1"},
		IsError:    true,
		Error:      errors.New("request failed with " + token),
		ErrorType:  "validation",
	}))
	require.Len(t, client.messages, 2)

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, true, capture.Properties[propertyIsError])
	assert.Equal(t, "validation", capture.Properties[propertyErrorType])
	assert.Equal(t, "request failed with [redacted]", capture.Properties[propertyErrorMessage])

	exception := requireException(t, client.messages[1])
	require.Len(t, exception.ExceptionList, 1)
	item := exception.ExceptionList[0]
	assert.Equal(t, "validation", item.Type)
	assert.Equal(t, "request failed with [redacted]", item.Value)
	require.NotNil(t, item.Mechanism)
	assert.Equal(t, true, *item.Mechanism.Handled)
	assert.Equal(t, true, *item.Mechanism.Synthetic)
	assert.Nil(t, item.Stacktrace)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, exception.Properties[propertyGroups])
}

func TestCaptureToolCallFailureDefaultsAndDisableFanout(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client, WithExceptionAutocapture(false)).CaptureToolCall(context.Background(), ToolCall{
		ToolName: "query",
		IsError:  true,
	}))
	require.Len(t, client.messages, 1)
	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "Error", capture.Properties[propertyErrorType])
	assert.Equal(t, "Tool query returned an error", capture.Properties[propertyErrorMessage])

}

func TestCaptureToolCallErrorSignals(t *testing.T) {
	tests := []struct {
		name          string
		call          ToolCall
		wantIsError   bool
		wantException bool
	}{
		{name: "success", call: ToolCall{ToolName: "query"}},
		{name: "IsError without Error", call: ToolCall{ToolName: "query", IsError: true}, wantIsError: true},
		{name: "Error without IsError", call: ToolCall{ToolName: "query", Error: errors.New("boom")}, wantIsError: true, wantException: true},
		{name: "IsError and Error", call: ToolCall{ToolName: "query", IsError: true, Error: errors.New("boom")}, wantIsError: true, wantException: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(context.Background(), tt.call))

			capture := requireCapture(t, client.messages[0])
			assert.Equal(t, tt.wantIsError, capture.Properties[propertyIsError])
			if tt.wantException {
				require.Len(t, client.messages, 2)
				assert.Equal(t, "boom", requireException(t, client.messages[1]).ExceptionList[0].Value)
			} else {
				assert.Len(t, client.messages, 1)
			}
		})
	}
}

func TestCaptureToolCallPanickingErrorFailsWithoutEnqueue(t *testing.T) {
	client := &fakeEnqueueClient{}
	err := New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", IsError: true, Error: panickingError{}})
	require.ErrorContains(t, err, "Error method panicked")
	assert.Empty(t, client.messages)
}

func TestCaptureToolCallEmptyErrorUsesFallbackMessage(t *testing.T) {
	for _, message := range []string{"", "   "} {
		client := &fakeEnqueueClient{}
		require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
			ToolName: "query",
			IsError:  true,
			Error:    errors.New(message),
		}))
		require.Len(t, client.messages, 2)
		capture := requireCapture(t, client.messages[0])
		assert.Equal(t, "Tool query returned an error", capture.Properties[propertyErrorMessage])
		exception := requireException(t, client.messages[1])
		assert.Equal(t, "Tool query returned an error", exception.ExceptionList[0].Value)
		assert.NoError(t, exception.Validate())
	}
}

func TestCaptureToolCallAttemptsAllEnqueues(t *testing.T) {
	client := &fakeEnqueueClient{errors: []error{errors.New("capture full"), errors.New("exception full")}}
	err := New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", Error: errors.New("boom")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "enqueue $mcp_tool_call: capture full")
	assert.ErrorContains(t, err, "enqueue $exception: exception full")
	assert.Len(t, client.messages, 2)
}

func TestCaptureToolCallKeepsEventsWithUnencodableValues(t *testing.T) {
	selfMap := map[string]any{"name": "loop"}
	selfMap["self"] = selfMap
	selfSlice := []any{"loop", nil}
	selfSlice[1] = selfSlice
	type reading struct{ Value float64 }

	for _, test := range []struct {
		name  string
		value any
		want  any
	}{
		{
			name:  "NaN and infinities",
			value: map[string]any{"nan": math.NaN(), "high": math.Inf(1), "ok": 1.5},
			want:  map[string]any{"nan": "[Unserializable: float64]", "high": "[Unserializable: float64]", "ok": json.Number("1.5")},
		},
		{
			name:  "nested map keeps encodable siblings",
			value: map[string]any{"filters": map[string]any{"limit": 10, "callback": func() {}}},
			want:  map[string]any{"filters": map[string]any{"limit": json.Number("10"), "callback": "[Unserializable: func()]"}},
		},
		{
			name:  "self-referential map",
			value: selfMap,
			want:  map[string]any{"name": "loop", "self": "[Circular ~]"},
		},
		{
			name:  "slice keeps encodable elements",
			value: map[string]any{"values": []any{1, "keep", math.NaN()}},
			want:  map[string]any{"values": []any{json.Number("1"), "keep", "[Unserializable: float64]"}},
		},
		{
			name:  "self-referential slice",
			value: selfSlice,
			want:  []any{"loop", "[Circular ~]"},
		},
		{
			name:  "struct with NaN field",
			value: reading{Value: math.NaN()},
			want:  "[Unserializable: posthogmcp.reading]",
		},
		{
			name:  "channel and panicking marshaler",
			value: map[string]any{"events": make(chan int), "value": panickingJSON{}},
			want:  map[string]any{"events": "[Unserializable: chan int]", "value": "[Unserializable: posthogmcp.panickingJSON]"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client, WithExceptionAutocapture(false)).CaptureToolCall(context.Background(), ToolCall{
				ToolName:   "query",
				Parameters: test.value,
				Response:   test.value,
				Properties: posthog.Properties{"payload": test.value},
			}))
			require.Len(t, client.messages, 1)
			capture := requireCapture(t, client.messages[0])
			assert.Equal(t, test.want, capture.Properties[propertyParameters])
			assert.Equal(t, test.want, capture.Properties[propertyResponse])
			assert.Equal(t, test.want, capture.Properties["payload"])
		})
	}
}

type panickingJSON struct{}

func (panickingJSON) MarshalJSON() ([]byte, error) { panic("secret payload") }

func TestCaptureToolCallWireGolden(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client, WithExceptionAutocapture(false)).CaptureToolCall(context.Background(), ToolCall{
		ToolName:        "search_docs",
		ToolDescription: "Search documentation",
		ToolCategory:    "Docs",
		DistinctID:      "user_1",
		SessionID:       "session_1",
		Groups:          posthog.Groups{"organization": "org_1"},
		SetProperties:   posthog.Properties{"plan": "pro"},
		ServerName:      "docs-server",
		ServerVersion:   "1.0.0",
		ClientName:      "test-client",
		ClientVersion:   "2.0.0",
		ProtocolVersion: "2026-07-28",
		Intent:          "Find setup instructions",
		IntentSource:    IntentSourceInferred,
		Parameters:      map[string]any{"query": "setup"},
		Response:        map[string]any{"content": []any{map[string]any{"type": "text", "text": "Found"}}},
		Duration:        42 * time.Millisecond,
		Properties:      posthog.Properties{"environment": "test"},
		Timestamp:       time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC),
	}))

	actual := normalizedCaptureWire(t, requireCapture(t, client.messages[0]))
	expected, err := os.ReadFile("testdata/tool_call.golden.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(actual))
}

func assertSerializedProperty(t *testing.T, message posthog.Message, key string, want any) {
	t.Helper()
	data, err := json.Marshal(message.APIfy())
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	properties, ok := payload["properties"].(map[string]any)
	require.True(t, ok, "properties type = %T", payload["properties"])
	if want == nil {
		assert.NotContains(t, properties, key)
		return
	}
	assert.Equal(t, want, properties[key])
}

func normalizedCaptureWire(t *testing.T, capture posthog.Capture) []byte {
	t.Helper()
	data, err := json.Marshal(capture.APIfy())
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	delete(wire, "uuid")
	delete(wire, "timestamp")
	properties := wire["properties"].(map[string]any)
	for _, key := range []string{"$lib", "$lib_version", "$go_version", "$os", "$os_version", "$os_distro", "$is_server"} {
		delete(properties, key)
	}
	result, err := json.MarshalIndent(wire, "", "  ")
	require.NoError(t, err)
	return result
}

func requireCapture(t *testing.T, message posthog.Message) posthog.Capture {
	t.Helper()
	capture, ok := message.(posthog.Capture)
	require.True(t, ok, "message type = %T", message)
	return capture
}

func requireException(t *testing.T, message posthog.Message) posthog.Exception {
	t.Helper()
	exception, ok := message.(posthog.Exception)
	require.True(t, ok, "message type = %T", message)
	return exception
}

func TestCaptureToolCallDistinctIDFromRequestContext(t *testing.T) {
	requestCtx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "request_user"})
	tests := []struct {
		name           string
		ctx            context.Context
		distinctID     string
		wantDistinctID string
	}{
		{name: "request context fills missing ID", ctx: requestCtx, wantDistinctID: "request_user"},
		{name: "request session ID stands in for a missing distinct ID", ctx: posthog.WithRequestContext(context.Background(), posthog.RequestContext{SessionId: "request_session"}), wantDistinctID: "request_session"},
		{name: "explicit ID wins", ctx: requestCtx, distinctID: "user_1", wantDistinctID: "user_1"},
		{name: "no request context", ctx: context.Background(), wantDistinctID: "anonymous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(tt.ctx, ToolCall{ToolName: "query", DistinctID: tt.distinctID}))
			assert.Equal(t, tt.wantDistinctID, requireCapture(t, client.messages[0]).DistinctId)
		})
	}
}

func TestCaptureToolCallSanitizesRequestContextProperties(t *testing.T) {
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"$current_url": "https://app.test/cb?token=abc",
		propertyIntent: "leak alice@example.com",
		propertySet:    map[string]any{"email": "leak@example.com"},
		"service":      "context",
	}})
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ctx, ToolCall{
		ToolName:   "query",
		Properties: posthog.Properties{"service": "call"},
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "https://app.test/cb?token=%5Bredacted%5D", capture.Properties["$current_url"])
	assert.Equal(t, "call", capture.Properties["service"], "call properties win over the request context")
	assert.NotContains(t, capture.Properties, propertyIntent)
	assert.NotContains(t, capture.Properties, propertySet)
}

func TestCaptureToolCallFallbackErrorMessageKeepsToolName(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "Get_Organization_Memberships", IsError: true}))
	assert.Equal(t, "Tool Get_Organization_Memberships returned an error", requireCapture(t, client.messages[0]).Properties[propertyErrorMessage])
}

func TestCaptureToolCallManualAPIFields(t *testing.T) {
	tests := []struct {
		name          string
		call          ToolCall
		wantToolCall  map[string]any
		wantException map[string]any
	}{
		{
			name:          "conversation id lands on both events",
			call:          ToolCall{ConversationID: "conv_1"},
			wantToolCall:  map[string]any{"$mcp_conversation_id": "conv_1"},
			wantException: map[string]any{"$mcp_conversation_id": "conv_1"},
		},
		{
			name:         "model defaults to self reported and is trimmed",
			call:         ToolCall{LLMModel: "  claude-opus-4  "},
			wantToolCall: map[string]any{"$mcp_llm_model": "claude-opus-4", "$mcp_llm_model_source": "self_reported"},
		},
		{
			name: "model keeps an explicit source",
			call: ToolCall{LLMModel: "gpt-5", LLMModelSource: ModelSourceClientMetadata},
			wantToolCall: map[string]any{
				"$mcp_llm_model":        "gpt-5",
				"$mcp_llm_model_source": "client_metadata",
			},
		},
		{name: "empty model is not recorded", call: ToolCall{LLMModel: "", LLMModelSource: ModelSourceClientMetadata}},
		{name: "blank model is not recorded", call: ToolCall{LLMModel: "   "}},
		{name: "unknown model is not recorded", call: ToolCall{LLMModel: " Unknown "}},
		{
			name:         "user agent and vendor client",
			call:         ToolCall{ClientUserAgent: "claude-code/2.1", VendorClient: "anthropic"},
			wantToolCall: map[string]any{"$mcp_client_user_agent": "claude-code/2.1", "$mcp_vendor_client": "anthropic"},
		},
		{
			name: "values are bounded to the metadata limit",
			call: ToolCall{
				ConversationID:  strings.Repeat("c", 300),
				LLMModel:        strings.Repeat("m", 300),
				ClientUserAgent: strings.Repeat("u", 300),
				VendorClient:    strings.Repeat("v", 300),
			},
			wantToolCall: map[string]any{
				"$mcp_conversation_id":   strings.Repeat("c", 253) + "...",
				"$mcp_llm_model":         strings.Repeat("m", 253) + "...",
				"$mcp_llm_model_source":  "self_reported",
				"$mcp_client_user_agent": strings.Repeat("u", 253) + "...",
				"$mcp_vendor_client":     strings.Repeat("v", 253) + "...",
			},
			wantException: map[string]any{"$mcp_conversation_id": strings.Repeat("c", 253) + "..."},
		},
		{
			name: "custom properties cannot set the reserved keys",
			call: ToolCall{Properties: posthog.Properties{
				"$mcp_conversation_id":   "wrong",
				"$mcp_llm_model":         "wrong",
				"$mcp_llm_model_source":  "wrong",
				"$mcp_client_user_agent": "wrong",
				"$mcp_vendor_client":     "wrong",
			}},
		},
	}
	keys := []string{
		"$mcp_conversation_id", "$mcp_llm_model", "$mcp_llm_model_source",
		"$mcp_client_user_agent", "$mcp_vendor_client",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			tt.call.ToolName = "query"
			tt.call.Error = errors.New("boom")
			require.NoError(t, New(client).CaptureToolCall(context.Background(), tt.call))
			require.Len(t, client.messages, 2)

			for _, event := range []struct {
				properties posthog.Properties
				want       map[string]any
			}{
				{requireCapture(t, client.messages[0]).Properties, tt.wantToolCall},
				{requireException(t, client.messages[1]).Properties, tt.wantException},
			} {
				for _, key := range keys {
					if value, ok := event.want[key]; ok {
						assert.Equal(t, value, event.properties[key], key)
					} else {
						assert.NotContains(t, event.properties, key)
					}
				}
			}
		})
	}
}

func TestCaptureToolCallRejectsInvalidLLMModelSource(t *testing.T) {
	client := &fakeEnqueueClient{}
	err := New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", LLMModelSource: "guess"})
	require.EqualError(t, err, "posthogmcp: invalid LLMModelSource")
	assert.Empty(t, client.messages)
}
