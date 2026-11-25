package posthogmcpgo

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var conversationHandle = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const echoedHandle = "0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f"

// deliveredHandle is the handle in the result's trailing conversation_id text
// block, or "" when it does not end with one.
func deliveredHandle(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	text, ok := result.Content[len(result.Content)-1].(mcp.TextContent)
	if !ok {
		return ""
	}
	var block map[string]string
	if json.Unmarshal([]byte(text.Text), &block) != nil || len(block) != 1 {
		return ""
	}
	return block["conversation_id"]
}

func TestConversationHandles(t *testing.T) {
	const (
		handle    = "handle"
		generated = "generated"
		transport = "transport"
	)
	for _, test := range []struct {
		name         string
		userAgent    string
		meta         map[string]any
		echo         string
		stateful     bool
		anchoringOff bool
		handlerErr   error
		wantSession  string
		wantSource   string
		wantHandle   bool
	}{
		{name: "a stateless call is minted a handle", userAgent: "example-client/1.0", wantSession: handle, wantSource: "conversation", wantHandle: true},
		{name: "an echoed handle sets the session", userAgent: "example-client/1.0", echo: echoedHandle, wantSession: "ses_6df45f0102a182bcd5e8dd5dad6c65a0", wantSource: "conversation"},
		{name: "an invalid codex id falls back to a handle", userAgent: "codex-mcp-client/0.50.0", meta: map[string]any{"sessionId": "not-a-uuid"}, wantSession: handle, wantSource: "conversation", wantHandle: true},
		{name: "chatgpt without its id gets no handle", userAgent: chatGPTUserAgent, wantSession: generated, wantSource: "generated"},
		{name: "a chatgpt id wins over an echoed handle", userAgent: chatGPTUserAgent, meta: map[string]any{"openai/session": "v1/conv-example-0001"}, echo: echoedHandle, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "a transport session gets no handle", userAgent: "example-client/1.0", stateful: true, wantSession: transport, wantSource: "transport"},
		{name: "anchoring off mints no handle", userAgent: "example-client/1.0", anchoringOff: true, wantSession: generated, wantSource: "generated"},
		{name: "a handle the result cannot carry is not used", userAgent: "example-client/1.0", handlerErr: errors.New("boom"), wantSession: generated, wantSource: "generated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			handler := &weatherHandler{err: test.handlerErr}
			s := newInstrumentedServer(queue, weatherTool(), handler, WithConversationID(!test.anchoringOff))
			var opts []server.StreamableHTTPOption
			if !test.stateful {
				opts = append(opts, server.WithStateLess(true))
			}
			c := connectHTTPAs(t, s, test.userAgent, "test-client", opts...)
			req := mcp.CallToolRequest{}
			req.Params.Name = "weather"
			arguments := map[string]any{"city": "Melbourne"}
			if test.echo != "" {
				arguments["conversation_id"] = test.echo
			}
			req.Params.Arguments = arguments
			if test.meta != nil {
				req.Params.Meta = &mcp.Meta{AdditionalFields: test.meta}
			}
			result, err := c.CallTool(t.Context(), req)
			if test.handlerErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			delivered := deliveredHandle(result)
			if test.wantHandle {
				require.Regexp(t, conversationHandle, delivered)
			} else {
				assert.Empty(t, delivered, "the result carries no conversation_id block")
			}
			if test.handlerErr == nil {
				require.Len(t, handler.received, 1)
				assert.NotContains(t, handler.received[0], "conversation_id", "the handler never sees conversation_id")
			}

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantSource, properties["$mcp_session_source"])
			switch test.wantSession {
			case handle:
				assert.Equal(t, posthogmcp.ConversationSessionID(delivered), properties["$session_id"])
				assert.Equal(t, delivered, properties["$mcp_conversation_id"])
				assert.NotContains(t, jsonString(t, properties["$mcp_response"]), "conversation_id", "the response is what the tool returned")
			case generated:
				assert.Regexp(t, generatedSessionID, properties["$session_id"])
				assert.NotContains(t, properties, "$mcp_conversation_id")
			case transport:
				assert.Equal(t, posthogmcp.TransportSessionID(c.GetSessionId()), properties["$session_id"])
			default:
				assert.Equal(t, test.wantSession, properties["$session_id"])
			}
		})
	}
}

func TestHandleFreeClientsAreNotAdvertisedAConversationID(t *testing.T) {
	for _, test := range []struct {
		name      string
		userAgent string
		want      bool
	}{
		{name: "chatgpt", userAgent: chatGPTUserAgent},
		{name: "codex", userAgent: "codex-mcp-client/0.50.0", want: true},
		{name: "another client", userAgent: "example-client/1.0", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newInstrumentedServer(&fakeQueue{}, weatherTool(), &weatherHandler{})
			c := connectHTTPAs(t, s, test.userAgent, "test-client", server.WithStateLess(true))
			listing, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
			require.NoError(t, err)
			require.Len(t, listing.Tools, 1)
			schema := listing.Tools[0].InputSchema
			_, advertised := schema.Properties["conversation_id"]
			assert.Equal(t, test.want, advertised)
			assert.Contains(t, schema.Properties, "context")
			assert.NotContains(t, schema.Required, "conversation_id", "the agent has no handle on its first call")
		})
	}
}

func TestConversationHandlesWhenTheMiddlewareCaptures(t *testing.T) {
	queue := &fakeQueue{}
	s := server.NewMCPServer("weather-server", "1.2.3",
		Instrument(posthogmcp.New(queue)), server.WithHooks(&server.Hooks{}))
	s.AddTool(weatherTool(), (&weatherHandler{}).handle)
	c := connectHTTPAs(t, s, "example-client/1.0", "test-client", server.WithStateLess(true))
	req := mcp.CallToolRequest{}
	req.Params.Name = "weather"
	req.Params.Arguments = map[string]any{"city": "Melbourne"}
	result, err := c.CallTool(t.Context(), req)
	require.NoError(t, err)

	handle := deliveredHandle(result)
	require.Regexp(t, conversationHandle, handle)
	properties := queue.onlyToolCall(t)
	assert.Equal(t, handle, properties["$mcp_conversation_id"])
	assert.Equal(t, posthogmcp.ConversationSessionID(handle), properties["$session_id"])
}
