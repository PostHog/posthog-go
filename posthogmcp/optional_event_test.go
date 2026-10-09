package posthogmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	posthog "github.com/posthog/posthog-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const optionalEventHandle = "0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f"

func captureOptional(t *testing.T, capture func(*Analytics) error) posthog.Capture {
	t.Helper()
	client := &fakeEnqueueClient{}
	require.NoError(t, capture(New(client)))
	require.Len(t, client.messages, 1, "an optional event is never a tool call or an exception")
	return requireCapture(t, client.messages[0])
}

func TestCaptureOptionalEventsCarryIdentityAndSession(t *testing.T) {
	shared := EventContext{
		DistinctID:      "user_1",
		Groups:          posthog.Groups{"organization": "org_1"},
		SetProperties:   posthog.Properties{"plan": "pro"},
		ServerName:      "server",
		ServerVersion:   "1.0",
		ClientName:      "client",
		ClientVersion:   "2.0",
		ProtocolVersion: "2026-07-28",
		ClientUserAgent: "client/2.0",
		VendorClient:    "vendor",
		ConversationID:  strings.ToUpper(optionalEventHandle),
		Properties:      posthog.Properties{"environment": "test", "$mcp_is_error": true},
		Timestamp:       time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC),
	}
	for _, test := range []struct {
		name    string
		event   string
		capture func(*Analytics) error
		extra   map[string]any
	}{
		{
			name:  "unknown tool",
			event: "$mcp_unknown_tool",
			capture: func(a *Analytics) error {
				return a.CaptureUnknownTool(context.Background(), UnknownTool{EventContext: shared, ToolName: "nope"})
			},
			extra: map[string]any{"$mcp_tool_name": "nope"},
		},
		{
			name:  "missing capability",
			event: "$mcp_missing_capability",
			capture: func(a *Analytics) error {
				return a.CaptureMissingCapability(context.Background(), MissingCapability{EventContext: shared, ToolName: "get_more_tools"})
			},
			extra: map[string]any{"$mcp_resource_name": "get_more_tools"},
		},
		{
			name:  "input required",
			event: "$mcp_input_required",
			capture: func(a *Analytics) error {
				return a.CaptureInputRequired(context.Background(), InputRequired{
					EventContext: shared,
					ToolName:     "deploy",
					Duration:     1500 * time.Microsecond,
					Methods:      []string{"elicitation/create"},
				})
			},
			extra: map[string]any{
				"$mcp_tool_name":             "deploy",
				"$mcp_duration_ms":           1.5,
				"$mcp_input_request_methods": []string{"elicitation/create"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureOptional(t, test.capture)

			assert.Equal(t, test.event, capture.Event)
			assert.Equal(t, "user_1", capture.DistinctId)
			assert.Equal(t, shared.Timestamp, capture.Timestamp)
			assert.Equal(t, posthog.Groups{"organization": "org_1"}, capture.Groups)
			want := map[string]any{
				"$mcp_conversation_id":   optionalEventHandle,
				"$session_id":            deriveSessionID(optionalEventHandle),
				"$mcp_server_name":       "server",
				"$mcp_server_version":    "1.0",
				"$mcp_client_name":       "client",
				"$mcp_client_version":    "2.0",
				"$mcp_protocol_version":  "2026-07-28",
				"$mcp_client_user_agent": "client/2.0",
				"$mcp_vendor_client":     "vendor",
				"environment":            "test",
				"$set":                   posthog.Properties{"plan": "pro"},
			}
			for key, value := range test.extra {
				want[key] = value
			}
			for key, value := range want {
				assert.Equal(t, value, capture.Properties[key], key)
			}
			for _, absent := range []string{"$mcp_is_error", "$mcp_source", "$mcp_parameters", "$mcp_response"} {
				assert.NotContains(t, capture.Properties, absent)
			}
		})
	}
}

func TestCaptureMissingCapabilityProperties(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  MissingCapability
		want   map[string]any
		absent []string
	}{
		{
			name:  "intent and model",
			event: MissingCapability{ToolName: "get_more_tools", Intent: "  export a dashboard to PDF ", LLMModel: "claude-opus-4-8"},
			want: map[string]any{
				"$mcp_resource_name":    "get_more_tools",
				"$mcp_intent":           "export a dashboard to PDF",
				"$mcp_intent_source":    "context_parameter",
				"$mcp_llm_model":        "claude-opus-4-8",
				"$mcp_llm_model_source": "self_reported",
			},
			absent: []string{"$mcp_tool_name", "$mcp_parameters", "$mcp_duration_ms", "$mcp_is_error", "$mcp_source", "$mcp_response"},
		},
		{
			name:  "model from client metadata",
			event: MissingCapability{ToolName: "get_more_tools", LLMModel: "gpt-5.2", LLMModelSource: ModelSourceClientMetadata},
			want:  map[string]any{"$mcp_llm_model": "gpt-5.2", "$mcp_llm_model_source": "client_metadata"},
		},
		{
			name:   "blank intent and unknown model are omitted",
			event:  MissingCapability{ToolName: "get_more_tools", Intent: " \n", LLMModel: "unknown"},
			absent: []string{"$mcp_intent", "$mcp_intent_source", "$mcp_llm_model", "$mcp_llm_model_source"},
		},
		{
			name:   "an empty JSON object is no intent",
			event:  MissingCapability{ToolName: "get_more_tools", Intent: "{}"},
			absent: []string{"$mcp_intent", "$mcp_intent_source"},
		},
		{
			name:  "intent is redacted as free text",
			event: MissingCapability{ToolName: "get_more_tools", Intent: "need a way to email jane.doe@example.com"},
			want:  map[string]any{"$mcp_intent": "need a way to email [redacted]"},
		},
		{
			name:  "intent is bounded",
			event: MissingCapability{ToolName: "get_more_tools", Intent: strings.Repeat("a", 3000)},
			want:  map[string]any{"$mcp_intent": strings.Repeat("a", 2045) + "..."},
		},
		{
			name:  "tool name is bounded",
			event: MissingCapability{ToolName: strings.Repeat("a", 300)},
			want:  map[string]any{"$mcp_resource_name": strings.Repeat("a", 253) + "..."},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureOptional(t, func(a *Analytics) error {
				return a.CaptureMissingCapability(context.Background(), test.event)
			})
			for key, value := range test.want {
				assert.Equal(t, value, capture.Properties[key], key)
			}
			for _, key := range test.absent {
				assert.NotContains(t, capture.Properties, key)
			}
		})
	}
}

func TestCaptureOptionalEventsFallBackToSessionAndRequestContext(t *testing.T) {
	requestContext := posthog.RequestContext{DistinctId: "request_user", SessionId: "request_session"}
	ctx := posthog.WithRequestContext(context.Background(), requestContext)

	capture := captureOptional(t, func(a *Analytics) error {
		return a.CaptureUnknownTool(ctx, UnknownTool{ToolName: "nope"})
	})
	assert.Equal(t, "request_user", capture.DistinctId)
	assert.Equal(t, "request_session", capture.Properties["$session_id"])

	capture = captureOptional(t, func(a *Analytics) error {
		return a.CaptureUnknownTool(context.Background(), UnknownTool{EventContext: EventContext{SessionID: "s1"}, ToolName: "nope"})
	})
	assert.Equal(t, "s1", capture.DistinctId)
	assert.Equal(t, false, capture.Properties["$process_person_profile"])
}

func TestCaptureUnknownToolNameIsSanitizedAsFreeText(t *testing.T) {
	for _, test := range []struct {
		name     string
		toolName string
		want     string
	}{
		{"plain name", "delete_everything", "delete_everything"},
		{"email in the name", "lookup jane.doe@example.com", "lookup [redacted]"},
		{"long name", strings.Repeat("a", 300), strings.Repeat("a", 253) + "..."},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureOptional(t, func(a *Analytics) error {
				return a.CaptureUnknownTool(context.Background(), UnknownTool{ToolName: test.toolName})
			})
			assert.Equal(t, test.want, capture.Properties["$mcp_tool_name"])
		})
	}
}

func TestCaptureInputRequiredMethods(t *testing.T) {
	many := make([]string, 120)
	for i := range many {
		many[i] = "elicitation/create"
	}
	for _, test := range []struct {
		name    string
		methods []string
		want    []string
	}{
		{"only request state", nil, []string{}},
		{"order and duplicates are kept", []string{"sampling/createMessage", "elicitation/create", "elicitation/create"}, []string{"sampling/createMessage", "elicitation/create", "elicitation/create"}},
		{"at most 100", many, many[:100]},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureOptional(t, func(a *Analytics) error {
				return a.CaptureInputRequired(context.Background(), InputRequired{ToolName: "deploy", Methods: test.methods})
			})
			assertSerializedProperty(t, capture, "$mcp_input_request_methods", anySlice(test.want))
		})
	}
}

func anySlice(values []string) []any {
	converted := make([]any, len(values))
	for i, value := range values {
		converted[i] = value
	}
	return converted
}

func TestCaptureOptionalEventsValidateAndStayBounded(t *testing.T) {
	oversized := posthog.Properties{"blob": strings.Repeat("x", 30_000), "b": strings.Repeat("y", 30_000), "c": strings.Repeat("z", 30_000), "d": strings.Repeat("w", 30_000)}
	for _, test := range []struct {
		name    string
		capture func(*Analytics) error
		wantErr string
	}{
		{
			name: "unknown tool without a name",
			capture: func(a *Analytics) error {
				return a.CaptureUnknownTool(context.Background(), UnknownTool{ToolName: " "})
			},
			wantErr: "ToolName must not be blank",
		},
		{
			name: "missing capability without a name",
			capture: func(a *Analytics) error {
				return a.CaptureMissingCapability(context.Background(), MissingCapability{ToolName: " "})
			},
			wantErr: "ToolName must not be blank",
		},
		{
			name: "input required with a negative duration",
			capture: func(a *Analytics) error {
				return a.CaptureInputRequired(context.Background(), InputRequired{ToolName: "deploy", Duration: -1})
			},
			wantErr: "Duration must not be negative",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			err := test.capture(New(client))
			require.ErrorContains(t, err, test.wantErr)
			assert.Empty(t, client.messages)
		})
	}

	t.Run("custom properties are dropped before the required ones", func(t *testing.T) {
		capture := captureOptional(t, func(a *Analytics) error {
			return a.CaptureInputRequired(context.Background(), InputRequired{
				EventContext: EventContext{Properties: oversized},
				ToolName:     "deploy",
			})
		})
		assert.NotContains(t, capture.Properties, "blob")
		assert.Equal(t, "deploy", capture.Properties["$mcp_tool_name"])
	})

	for _, test := range []struct {
		name    string
		capture func(*Analytics) error
	}{
		{"unknown tool", func(a *Analytics) error {
			return a.CaptureUnknownTool(context.Background(), UnknownTool{
				EventContext: EventContext{DistinctID: "user_1", SetProperties: oversized}, ToolName: "nope"})
		}},
		{"input required", func(a *Analytics) error {
			return a.CaptureInputRequired(context.Background(), InputRequired{
				EventContext: EventContext{DistinctID: "user_1", SetProperties: oversized}, ToolName: "deploy"})
		}},
		{"missing capability", func(a *Analytics) error {
			return a.CaptureMissingCapability(context.Background(), MissingCapability{
				EventContext: EventContext{DistinctID: "user_1", SetProperties: oversized}, ToolName: "get_more_tools"})
		}},
	} {
		t.Run(test.name+" drops an oversized $set last", func(t *testing.T) {
			capture := captureOptional(t, test.capture)
			assert.Equal(t, "user_1", capture.DistinctId)
			assert.NotContains(t, capture.Properties, "$set")
		})
	}

	t.Run("input request methods are redacted like other strings", func(t *testing.T) {
		capture := captureOptional(t, func(a *Analytics) error {
			return a.CaptureInputRequired(context.Background(), InputRequired{
				ToolName: "deploy",
				Methods:  []string{"elicitation/create", "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a"},
			})
		})
		methods, ok := capture.Properties["$mcp_input_request_methods"].([]string)
		require.True(t, ok)
		require.Len(t, methods, 2)
		assert.Equal(t, "elicitation/create", methods[0])
		assert.NotContains(t, methods[1], "16C7e42F292c6912E7710c838347Ae178B4a")
	})

	t.Run("nil analytics", func(t *testing.T) {
		var a *Analytics
		require.Error(t, a.CaptureUnknownTool(context.Background(), UnknownTool{ToolName: "nope"}))
		require.Error(t, a.CaptureInputRequired(context.Background(), InputRequired{ToolName: "deploy"}))
		require.Error(t, a.CaptureMissingCapability(context.Background(), MissingCapability{ToolName: "get_more_tools"}))
	})
}
