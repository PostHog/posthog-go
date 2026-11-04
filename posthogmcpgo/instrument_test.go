package posthogmcpgo

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var generatedSessionID = regexp.MustCompile(`^ses_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type fakeQueue struct {
	mu       sync.Mutex
	messages []posthog.Message
}

func (q *fakeQueue) Enqueue(message posthog.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
	return nil
}

func (q *fakeQueue) toolCalls() []posthog.Capture {
	q.mu.Lock()
	defer q.mu.Unlock()
	var captures []posthog.Capture
	for _, message := range q.messages {
		if capture, ok := message.(posthog.Capture); ok && capture.Event == "$mcp_tool_call" {
			captures = append(captures, capture)
		}
	}
	return captures
}

func (q *fakeQueue) exceptions() []posthog.Exception {
	q.mu.Lock()
	defer q.mu.Unlock()
	var exceptions []posthog.Exception
	for _, message := range q.messages {
		if exception, ok := message.(posthog.Exception); ok {
			exceptions = append(exceptions, exception)
		}
	}
	return exceptions
}

func (q *fakeQueue) onlyToolCall(t *testing.T) posthog.Properties {
	t.Helper()
	captures := q.toolCalls()
	require.Len(t, captures, 1)
	return captures[0].Properties
}

// weatherHandler answers like a tool and records the arguments it received.
type weatherHandler struct {
	result *mcp.CallToolResult
	err    error

	mu       sync.Mutex
	received []map[string]any
	bound    []map[string]any
}

func (h *weatherHandler) handle(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var bound map[string]any
	if err := req.BindArguments(&bound); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.received = append(h.received, req.GetArguments())
	h.bound = append(h.bound, bound)
	h.mu.Unlock()
	if h.result == nil && h.err == nil {
		return mcp.NewToolResultText("Sunny, 22C"), nil
	}
	return h.result, h.err
}

func weatherTool(opts ...mcp.ToolOption) mcp.Tool {
	tool := mcp.NewTool("weather", append([]mcp.ToolOption{
		mcp.WithDescription("Get current weather"),
		mcp.WithString("city", mcp.Required()),
	}, opts...)...)
	tool.Meta = &mcp.Meta{AdditionalFields: map[string]any{"category": "forecasts"}}
	return tool
}

func newInstrumentedServer(queue *fakeQueue, tool mcp.Tool, handler *weatherHandler, opts ...Option) *server.MCPServer {
	s := server.NewMCPServer("weather-server", "1.2.3",
		server.WithToolCapabilities(true),
		Instrument(posthogmcp.New(queue), append([]Option{WithServerInfo("weather-server", "1.2.3")}, opts...)...),
	)
	s.AddTool(tool, handler.handle)
	return s
}

func connectInProcess(t *testing.T, s *server.MCPServer) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(s)
	require.NoError(t, err)
	return initialize(t, c)
}

func initialize(t *testing.T, c *client.Client) *client.Client {
	t.Helper()
	require.NoError(t, c.Start(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test-client", Version: "2.0.0"}
	_, err := c.Initialize(t.Context(), init)
	require.NoError(t, err)
	return c
}

func callTool(t *testing.T, c *client.Client, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments
	return c.CallTool(t.Context(), req)
}

func jsonString(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func TestInstrumentCapturesToolCallEndToEnd(t *testing.T) {
	queue := &fakeQueue{}
	handler := &weatherHandler{}
	s := newInstrumentedServer(queue, weatherTool(), handler,
		WithIdentity(func(context.Context, mcp.CallToolRequest) (Identity, error) {
			return Identity{
				DistinctID:    "user-123",
				Groups:        posthog.Groups{"company": "acme"},
				SetProperties: posthog.Properties{"plan": "pro"},
			}, nil
		}),
		WithProperties(func(context.Context, mcp.CallToolRequest, *mcp.CallToolResult, error) (posthog.Properties, error) {
			return posthog.Properties{"environment": "test"}, nil
		}),
	)
	c := connectInProcess(t, s)

	result, err := callTool(t, c, "weather", map[string]any{
		"city":      "Melbourne",
		"context":   "Checking the weather before a trip",
		"llm_model": "claude-opus-4-8",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	captures := queue.toolCalls()
	require.Len(t, captures, 1)
	assert.Equal(t, "user-123", captures[0].DistinctId)
	properties := captures[0].Properties
	assert.IsType(t, float64(0), properties["$mcp_duration_ms"])
	delete(properties, "$mcp_duration_ms")
	assert.Regexp(t, generatedSessionID, properties["$session_id"])
	delete(properties, "$session_id")
	assert.JSONEq(t, `{
		"$groups": {"company": "acme"},
		"$mcp_client_name": "test-client",
		"$mcp_client_version": "2.0.0",
		"$mcp_intent": "Checking the weather before a trip",
		"$mcp_intent_source": "context_parameter",
		"$mcp_is_error": false,
		"$mcp_llm_model": "claude-opus-4-8",
		"$mcp_llm_model_source": "self_reported",
		"$mcp_parameters": {"request": {"method": "tools/call", "params": {"name": "weather", "arguments": {"city": "Melbourne"}}}},
		"$mcp_resource_name": "weather",
		"$mcp_response": {"content": [{"type": "text", "text": "Sunny, 22C"}]},
		"$mcp_server_name": "weather-server",
		"$mcp_server_version": "1.2.3",
		"$mcp_source": "posthog_mcp_analytics",
		"$mcp_tool_category": "forecasts",
		"$mcp_tool_description": "Get current weather",
		"$mcp_tool_name": "weather",
		"$set": {"plan": "pro"},
		"environment": "test"
	}`, jsonString(t, properties))
	assert.Empty(t, queue.exceptions())
}

func TestInstrumentRemovesAnalyticsArgumentsBeforeTheHandler(t *testing.T) {
	for _, test := range []struct {
		name       string
		tool       mcp.Tool
		opts       []Option
		arguments  map[string]any
		wantSeen   map[string]any
		wantIntent any
		wantModel  any
	}{
		{
			name:       "injected arguments are removed",
			tool:       weatherTool(),
			arguments:  map[string]any{"city": "Melbourne", "context": "Planning a trip", "llm_model": "gpt-5.2"},
			wantSeen:   map[string]any{"city": "Melbourne"},
			wantIntent: "Planning a trip",
			wantModel:  "gpt-5.2",
		},
		{
			name:       "a tool's own context and llm_model are kept, and its context is still the intent",
			tool:       weatherTool(mcp.WithString("context"), mcp.WithString("llm_model")),
			arguments:  map[string]any{"city": "Melbourne", "context": "Planning a trip", "llm_model": "gpt-5.2"},
			wantSeen:   map[string]any{"city": "Melbourne", "context": "Planning a trip", "llm_model": "gpt-5.2"},
			wantIntent: "Planning a trip",
		},
		{
			name:      "disabled arguments are not removed or read",
			tool:      weatherTool(),
			opts:      []Option{WithContextParameter(false), WithCaptureModel(false)},
			arguments: map[string]any{"city": "Melbourne", "context": "Planning a trip", "llm_model": "gpt-5.2"},
			wantSeen:  map[string]any{"city": "Melbourne", "context": "Planning a trip", "llm_model": "gpt-5.2"},
		},
		{
			name:      "an unknown model is not recorded",
			tool:      weatherTool(),
			arguments: map[string]any{"city": "Melbourne", "llm_model": "unknown"},
			wantSeen:  map[string]any{"city": "Melbourne"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			handler := &weatherHandler{}
			c := connectInProcess(t, newInstrumentedServer(queue, test.tool, handler, test.opts...))

			_, err := callTool(t, c, "weather", test.arguments)
			require.NoError(t, err)

			require.Len(t, handler.received, 1)
			assert.Equal(t, test.wantSeen, handler.received[0], "GetArguments")
			assert.Equal(t, test.wantSeen, handler.bound[0], "BindArguments")
			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantIntent, properties["$mcp_intent"])
			assert.Equal(t, test.wantModel, properties["$mcp_llm_model"])
		})
	}
}

func TestInstrumentAdvertisesAnalyticsArguments(t *testing.T) {
	for _, test := range []struct {
		name           string
		tool           mcp.Tool
		opts           []Option
		wantProperties []string
		wantRequired   []string
	}{
		{
			name:           "added as required",
			tool:           weatherTool(),
			wantProperties: []string{"city", "context", "llm_model"},
			wantRequired:   []string{"city", "context", "llm_model"},
		},
		{
			name:           "added to a raw schema that disallows other properties",
			tool:           mcp.NewToolWithRawSchema("weather", "Get current weather", json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
			wantProperties: []string{"city", "context", "llm_model"},
			wantRequired:   []string{"city", "context", "llm_model"},
		},
		{
			name:           "a tool's own context is left as declared",
			tool:           weatherTool(mcp.WithString("context")),
			wantProperties: []string{"city", "context", "llm_model"},
			wantRequired:   []string{"city", "llm_model"},
		},
		{
			name:           "only the enabled arguments",
			tool:           weatherTool(),
			opts:           []Option{WithCaptureModel(false)},
			wantProperties: []string{"city", "context"},
			wantRequired:   []string{"city", "context"},
		},
		{
			name:           "a schema with a root combinator is left alone",
			tool:           mcp.NewToolWithRawSchema("weather", "Get current weather", json.RawMessage(`{"type":"object","oneOf":[{"properties":{"city":{"type":"string"}}}]}`)),
			wantProperties: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := connectInProcess(t, newInstrumentedServer(&fakeQueue{}, test.tool, &weatherHandler{}, test.opts...))

			listed, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
			require.NoError(t, err)
			require.Len(t, listed.Tools, 1)
			var schema struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			}
			require.NoError(t, json.Unmarshal([]byte(jsonString(t, listed.Tools[0])), &struct {
				InputSchema any `json:"inputSchema"`
			}{&schema}))
			assert.ElementsMatch(t, test.wantProperties, keys(schema.Properties))
			assert.ElementsMatch(t, test.wantRequired, schema.Required)
		})
	}
}

func keys(m map[string]any) []string {
	var names []string
	for name := range m {
		names = append(names, name)
	}
	return names
}

func TestInstrumentCapturesFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		handler     *weatherHandler
		wantMessage string
		wantRPCErr  bool
	}{
		{
			name:        "an isError result",
			handler:     &weatherHandler{result: mcp.NewToolResultError("city not found")},
			wantMessage: "city not found",
		},
		{
			name:        "a returned error",
			handler:     &weatherHandler{err: errors.New("upstream timeout")},
			wantMessage: "upstream timeout",
			wantRPCErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			c := connectInProcess(t, newInstrumentedServer(queue, weatherTool(), test.handler))

			_, err := callTool(t, c, "weather", map[string]any{"city": "Atlantis", "context": "Checking a forecast"})
			assert.Equal(t, test.wantRPCErr, err != nil, "the client sees the handler's outcome unchanged: %v", err)

			properties := queue.onlyToolCall(t)
			assert.Equal(t, true, properties["$mcp_is_error"])
			assert.Equal(t, test.wantMessage, properties["$mcp_error_message"])
			exceptions := queue.exceptions()
			require.Len(t, exceptions, 1)
			assert.Equal(t, properties["$session_id"], exceptions[0].Properties["$session_id"])
		})
	}
}

func TestInstrumentOmitsDisabledPayloads(t *testing.T) {
	queue := &fakeQueue{}
	c := connectInProcess(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{},
		WithCaptureParameters(false), WithCaptureResponses(false)))

	_, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
	require.NoError(t, err)

	properties := queue.onlyToolCall(t)
	assert.NotContains(t, properties, "$mcp_parameters")
	assert.NotContains(t, properties, "$mcp_response")
}

func TestInstrumentReportsInstrumentationErrorsWithoutChangingTheCall(t *testing.T) {
	queue := &fakeQueue{}
	var reported []error
	c := connectInProcess(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{},
		WithIdentity(func(context.Context, mcp.CallToolRequest) (Identity, error) { panic("boom") }),
		WithErrorHandler(func(_ context.Context, err error) { reported = append(reported, err) }),
	))

	result, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, reported, 1)
	assert.ErrorContains(t, reported[0], "identity resolver panic")
	assert.Len(t, queue.toolCalls(), 1)
}
