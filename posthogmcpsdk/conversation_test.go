package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var conversationHandle = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The cross-SDK vector from the MCP analytics spec.
const (
	vectorHandle    = "0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f"
	vectorSessionID = "ses_6df45f0102a182bcd5e8dd5dad6c65a0"
)

type transport int

const (
	statelessHTTP transport = iota
	statefulHTTP
	inMemory
)

func connect(t *testing.T, server *mcpsdk.Server, over transport) *mcpsdk.ClientSession {
	t.Helper()
	if over == inMemory {
		return connectInMemory(t, server)
	}
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{Stateless: over == statelessHTTP},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// newSessionlessServer issues no Mcp-Session-Id, so over stateless HTTP no
// request carries a transport session.
func newSessionlessServer() *mcpsdk.Server {
	return mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"},
		&mcpsdk.ServerOptions{GetSessionID: func() string { return "" }},
	)
}

// deliveredHandle is the handle in the result's trailing conversation_id
// text block, or "" when the result does not end with one.
func deliveredHandle(t *testing.T, result *mcpsdk.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		return ""
	}
	text, ok := result.Content[len(result.Content)-1].(*mcpsdk.TextContent)
	if !ok {
		return ""
	}
	var block map[string]string
	if json.Unmarshal([]byte(text.Text), &block) != nil || len(block) != 1 {
		return ""
	}
	return block["conversation_id"]
}

func TestStatelessHTTPConversationHandleSharesASession(t *testing.T) {
	queue := &fakeQueue{}
	server := newSessionlessServer()
	Instrument(server, posthogmcp.New(queue))
	addWeatherTool(server, nil)
	session := connect(t, server, statelessHTTP)
	_, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	first, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	})
	require.NoError(t, err)
	handle := deliveredHandle(t, first)
	require.Regexp(t, conversationHandle, handle)
	require.Len(t, first.Content, 2)
	assert.JSONEq(t, `{"temperature":21,"_mcp_instructions":{"conversation_id":"`+handle+`"}}`, jsonString(t, first.StructuredContent))

	second, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne", "conversation_id": handle},
	})
	require.NoError(t, err)
	require.False(t, second.IsError, "the typed tool received the injected conversation_id")
	assert.Len(t, second.Content, 1, "an echoed handle is not delivered again")
	assert.JSONEq(t, `{"temperature":21,"_mcp_instructions":{"conversation_id":"`+handle+`"}}`, jsonString(t, second.StructuredContent))

	captures := queue.toolCalls()
	require.Len(t, captures, 2)
	for _, capture := range captures {
		assert.Equal(t, deterministicSessionID(handle), capture.Properties["$session_id"])
		assert.Equal(t, handle, capture.Properties["$mcp_conversation_id"])
		assert.JSONEq(t, `{"city":"Melbourne"}`, jsonString(t, capture.Properties["$mcp_parameters"].(map[string]any)["request"].(map[string]any)["params"].(map[string]any)["arguments"]))
	}
}

func TestInstrumentConversationHandle(t *testing.T) {
	ownConversation := map[string]any{"type": "object", "properties": map[string]any{"conversation_id": map[string]any{"type": "string"}}}
	for _, test := range []struct {
		name             string
		over             transport
		opts             []Option
		inputSchema      any
		arguments        map[string]any
		wantArguments    string
		wantMinted       bool
		wantConversation any
		wantSessionID    any
	}{
		{
			name:          "a request without a session or handle gets a new handle",
			over:          statelessHTTP,
			arguments:     map[string]any{"query": "flags"},
			wantArguments: `{"query":"flags"}`,
			wantMinted:    true,
		},
		{
			name:          "a malformed handle is replaced and never reaches the tool",
			over:          statelessHTTP,
			arguments:     map[string]any{"query": "flags", "conversation_id": "not-a-uuid"},
			wantArguments: `{"query":"flags"}`,
			wantMinted:    true,
		},
		{
			name:             "an echoed handle derives the canonical session",
			over:             statelessHTTP,
			arguments:        map[string]any{"query": "flags", "conversation_id": " 0190F0E8-7A6B-7C3D-9E4F-5A6B7C8D9E0F "},
			wantArguments:    `{"query":"flags"}`,
			wantConversation: vectorHandle,
			wantSessionID:    vectorSessionID,
		},
		{
			name:          "a one-client connection keeps its session",
			over:          inMemory,
			arguments:     map[string]any{"query": "flags"},
			wantArguments: `{"query":"flags"}`,
		},
		{
			name:             "an echoed handle wins over a one-client connection",
			over:             inMemory,
			arguments:        map[string]any{"conversation_id": vectorHandle},
			wantArguments:    `{}`,
			wantConversation: vectorHandle,
			wantSessionID:    vectorSessionID,
		},
		{
			name:          "an HTTP session keeps its session",
			over:          statefulHTTP,
			arguments:     map[string]any{"query": "flags"},
			wantArguments: `{"query":"flags"}`,
		},
		{
			name:          "disabled",
			over:          statelessHTTP,
			opts:          []Option{WithConversationID(false)},
			arguments:     map[string]any{"conversation_id": vectorHandle},
			wantArguments: `{"conversation_id":"` + vectorHandle + `"}`,
		},
		{
			name:          "a tool's own conversation_id is its data",
			over:          statelessHTTP,
			inputSchema:   ownConversation,
			arguments:     map[string]any{"conversation_id": vectorHandle},
			wantArguments: `{"conversation_id":"` + vectorHandle + `"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newSessionlessServer()
			if test.over == statefulHTTP {
				server = newServer()
			}
			Instrument(server, posthogmcp.New(queue), test.opts...)
			inputSchema := test.inputSchema
			if inputSchema == nil {
				inputSchema = map[string]any{"type": "object"}
			}
			argumentsEchoTool(server, inputSchema)

			result, err := connect(t, server, test.over).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "echo",
				Arguments: test.arguments,
			})
			require.NoError(t, err)
			assert.JSONEq(t, test.wantArguments, result.Content[0].(*mcpsdk.TextContent).Text)

			properties := queue.onlyToolCall(t)
			assert.JSONEq(t, test.wantArguments, jsonString(t, properties["$mcp_parameters"].(map[string]any)["request"].(map[string]any)["params"].(map[string]any)["arguments"]))
			if test.wantMinted {
				require.Len(t, result.Content, 2)
				handle := deliveredHandle(t, result)
				require.Regexp(t, conversationHandle, handle)
				assert.Equal(t, handle, properties["$mcp_conversation_id"])
				assert.Equal(t, deterministicSessionID(handle), properties["$session_id"])
				return
			}
			assert.Len(t, result.Content, 1)
			assert.Equal(t, test.wantConversation, properties["$mcp_conversation_id"])
			if test.wantSessionID != nil {
				assert.Equal(t, test.wantSessionID, properties["$session_id"])
			}
		})
	}
}

func TestInstrumentConversationHandleOnFailures(t *testing.T) {
	for _, test := range []struct {
		name          string
		addTool       func(*mcpsdk.Server)
		wantDelivered bool
		wantMessage   string
	}{
		{
			name:          "an isError result carries a new handle",
			addTool:       func(server *mcpsdk.Server) { addWeatherTool(server, errors.New("forecast unavailable")) },
			wantDelivered: true,
			wantMessage:   "forecast unavailable",
		},
		{
			name: "a protocol error carries none, so none is recorded",
			addTool: func(server *mcpsdk.Server) {
				server.AddTool(
					&mcpsdk.Tool{Name: "weather", InputSchema: map[string]any{"type": "object"}},
					func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
						return nil, errors.New("handler failed")
					},
				)
			},
			wantMessage: "handler failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newSessionlessServer()
			Instrument(server, posthogmcp.New(queue))
			test.addTool(server)

			result, err := connect(t, server, statelessHTTP).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "weather",
				Arguments: map[string]any{"city": "Melbourne"},
			})

			properties := queue.onlyToolCall(t)
			exceptions := queue.exceptions()
			require.Len(t, exceptions, 1)
			assert.Equal(t, test.wantMessage, properties["$mcp_error_message"])
			if !test.wantDelivered {
				require.Error(t, err)
				assert.NotContains(t, properties, "$mcp_conversation_id")
				assert.NotContains(t, exceptions[0].Properties, "$mcp_conversation_id")
				return
			}
			require.NoError(t, err)
			require.True(t, result.IsError)
			handle := deliveredHandle(t, result)
			require.Regexp(t, conversationHandle, handle)
			assert.Equal(t, handle, properties["$mcp_conversation_id"])
			assert.Equal(t, handle, exceptions[0].Properties["$mcp_conversation_id"])
		})
	}
}

func TestInstrumentDeclaresConversationInstructions(t *testing.T) {
	instructions := map[string]any{
		"type":        "object",
		"description": "Server-issued metadata for this conversation.",
		"properties": map[string]any{
			"conversation_id": map[string]any{"type": "string", "description": "The server-issued conversation identifier."},
		},
	}
	plain := map[string]any{"type": "object", "properties": map[string]any{"temperature": map[string]any{"type": "integer"}}}
	for _, test := range []struct {
		name             string
		opts             []Option
		inputSchema      any
		outputSchema     any
		wantOutputSchema any
	}{
		{
			name:         "declared on a plain object",
			outputSchema: plain,
			wantOutputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"temperature":       map[string]any{"type": "integer"},
				"_mcp_instructions": instructions,
			}},
		},
		{
			name:             "a tool's own _mcp_instructions is kept",
			outputSchema:     map[string]any{"type": "object", "properties": map[string]any{"_mcp_instructions": map[string]any{"type": "string"}}},
			wantOutputSchema: map[string]any{"type": "object", "properties": map[string]any{"_mcp_instructions": map[string]any{"type": "string"}}},
		},
		{
			name:             "combinator schemas are left alone",
			outputSchema:     map[string]any{"type": "object", "oneOf": []any{plain}},
			wantOutputSchema: map[string]any{"type": "object", "oneOf": []any{plain}},
		},
		{
			name: "no output schema",
		},
		{
			name:             "not without an injected conversation_id",
			inputSchema:      map[string]any{"type": "object", "properties": map[string]any{"conversation_id": map[string]any{"type": "string"}}},
			outputSchema:     plain,
			wantOutputSchema: plain,
		},
		{
			name:             "disabled",
			opts:             []Option{WithConversationID(false)},
			outputSchema:     plain,
			wantOutputSchema: plain,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newServer()
			Instrument(server, posthogmcp.New(&fakeQueue{}), test.opts...)
			inputSchema := test.inputSchema
			if inputSchema == nil {
				inputSchema = map[string]any{"type": "object"}
			}
			server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: inputSchema, OutputSchema: test.outputSchema}, echoHandler)

			result, err := connectInMemory(t, server).ListTools(t.Context(), nil)
			require.NoError(t, err)
			require.Len(t, result.Tools, 1)
			assert.JSONEq(t, jsonString(t, test.wantOutputSchema), jsonString(t, result.Tools[0].OutputSchema))
		})
	}
}

// go-sdk v1.8 returns an input_required round as a result with
// InputRequests, which must carry no content, so a new handle cannot ride it.
func TestMiddlewareDoesNotDeliverAHandleOnAnInputRequiredRound(t *testing.T) {
	result := &mcpsdk.CallToolResult{}
	inputRequests := reflect.ValueOf(result).Elem().FieldByName("InputRequests")
	if !inputRequests.IsValid() {
		t.Skip("this go-sdk has no multi round-trip results")
	}
	inputRequests.Set(reflect.MakeMap(inputRequests.Type()))

	queue := &fakeQueue{}
	receive := NewMiddleware(posthogmcp.New(queue)).Receiving(func(_ context.Context, method string, _ mcpsdk.Request) (mcpsdk.Result, error) {
		if method == methodListTools {
			return listing("ask"), nil
		}
		return result, nil
	})
	_, err := receive(t.Context(), methodListTools, &mcpsdk.ListToolsRequest{Params: &mcpsdk.ListToolsParams{}})
	require.NoError(t, err)

	got, err := receive(t.Context(), methodCallTool, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Name: "ask", Arguments: json.RawMessage(`{}`)},
		Extra:  &mcpsdk.RequestExtra{},
	})
	require.NoError(t, err)
	assert.Same(t, result, got)
	for _, capture := range queue.toolCalls() {
		assert.NotContains(t, capture.Properties, "$mcp_conversation_id")
	}
}
