package posthogmcpsdk

import (
	"cmp"
	"context"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cannedMissingCapabilityText = "Unfortunately, we have shown you the full tool list. We have noted your feedback " +
	"and will work to improve the tool list in the future."

func listedTools(t *testing.T, session *mcpsdk.ClientSession) []*mcpsdk.Tool {
	t.Helper()
	var tools []*mcpsdk.Tool
	for tool, err := range session.Tools(t.Context(), nil) {
		require.NoError(t, err)
		tools = append(tools, tool)
	}
	return tools
}

func toolNames(tools []*mcpsdk.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

func inputSchemaOf(t *testing.T, tool *mcpsdk.Tool) (properties map[string]any, required []any) {
	t.Helper()
	schema, ok := tool.InputSchema.(map[string]any)
	require.True(t, ok)
	properties, _ = schema["properties"].(map[string]any)
	required, _ = schema["required"].([]any)
	return properties, required
}

func TestMissingCapabilityToolIsListed(t *testing.T) {
	for _, test := range []struct {
		name       string
		opts       []Option
		pageSize   int
		extraTool  string
		registered string
		wantNames  []string
	}{
		{name: "off by default", wantNames: []string{"weather"}},
		{name: "on with the default name", opts: []Option{WithMissingCapabilityTool("")}, wantNames: []string{"weather", "get_more_tools"}},
		{name: "on with a custom name", opts: []Option{WithMissingCapabilityTool("request_tool")}, wantNames: []string{"weather", "request_tool"}},
		{name: "a name with spaces around it is trimmed", opts: []Option{WithMissingCapabilityTool("  request_tool ")}, wantNames: []string{"weather", "request_tool"}},
		{name: "a blank name is the default", opts: []Option{WithMissingCapabilityTool("  ")}, wantNames: []string{"weather", "get_more_tools"}},
		{
			name:       "not duplicated when the server registers the name",
			opts:       []Option{WithMissingCapabilityTool("")},
			registered: "get_more_tools",
			wantNames:  []string{"weather", "get_more_tools"},
		},
		{
			name:      "once, on the last page",
			opts:      []Option{WithMissingCapabilityTool("")},
			pageSize:  1,
			extraTool: "search",
			wantNames: []string{"weather", "search", "get_more_tools"},
		},
		{
			name:       "not duplicated when the server registers the name on an earlier page",
			opts:       []Option{WithMissingCapabilityTool("")},
			pageSize:   1,
			extraTool:  "search",
			registered: "get_more_tools",
			wantNames:  []string{"get_more_tools", "weather", "search"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, &mcpsdk.ServerOptions{PageSize: test.pageSize})
			Instrument(server, posthogmcp.New(&fakeQueue{}), test.opts...)
			for _, name := range []string{test.registered, test.extraTool} {
				if name != "" {
					server.AddTool(&mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
						func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
							return &mcpsdk.CallToolResult{}, nil
						})
				}
			}
			addWeatherTool(server, nil)

			assert.ElementsMatch(t, test.wantNames, toolNames(listedTools(t, connectInMemory(t, server))))
		})
	}
}

func TestMissingCapabilityToolSchema(t *testing.T) {
	for _, test := range []struct {
		name         string
		opts         []Option
		wantProperty []string
		wantRequired []any
	}{
		{
			name:         "every enabled argument",
			wantProperty: []string{"context", "llm_model", "conversation_id"},
			wantRequired: []any{"context", "llm_model"},
		},
		{
			name:         "without the model argument",
			opts:         []Option{WithCaptureModel(false)},
			wantProperty: []string{"context", "conversation_id"},
			wantRequired: []any{"context"},
		},
		{
			name:         "without the disabled injected arguments",
			opts:         []Option{WithCaptureModel(false), WithConversationID(false)},
			wantProperty: []string{"context"},
			wantRequired: []any{"context"},
		},
		{
			name:         "with its own context even when the context parameter is off",
			opts:         []Option{WithContextParameter(false), WithCaptureModel(false), WithConversationID(false)},
			wantProperty: []string{"context"},
			wantRequired: []any{"context"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newServer()
			Instrument(server, posthogmcp.New(&fakeQueue{}), append([]Option{WithMissingCapabilityTool("")}, test.opts...)...)

			tools := listedTools(t, connectInMemory(t, server))

			require.Len(t, tools, 1)
			properties, required := inputSchemaOf(t, tools[0])
			assert.ElementsMatch(t, test.wantProperty, mapKeys(properties))
			assert.ElementsMatch(t, test.wantRequired, required)
			assert.Equal(t, "Check for additional tools whenever your task might benefit from specialized capabilities - even if existing tools could work as a fallback.", tools[0].Description)
		})
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func TestMissingCapabilityToolCall(t *testing.T) {
	const report = "export a dashboard to PDF"
	for _, test := range []struct {
		name             string
		over             transport
		opts             []Option
		call             string
		meta             mcpsdk.Meta
		arguments        map[string]any
		wantIntent       any
		wantModel        any
		wantModelSource  any
		wantConversation func(t *testing.T, delivered, recorded any)
	}{
		{
			name:       "the report is the intent",
			over:       inMemory,
			arguments:  map[string]any{"context": "  " + report + " "},
			wantIntent: report,
		},
		{
			name:       "the intent is redacted as free text",
			over:       inMemory,
			arguments:  map[string]any{"context": "email jane.doe@example.com a report"},
			wantIntent: "email [redacted] a report",
		},
		{name: "a blank report has no intent", over: inMemory, arguments: map[string]any{"context": "  "}},
		{name: "a missing report has no intent", over: inMemory, arguments: map[string]any{}},
		{name: "a report that is not a string has no intent", over: inMemory, arguments: map[string]any{"context": 42}},
		{
			name:            "the model the agent reports",
			over:            inMemory,
			arguments:       map[string]any{"context": report, "llm_model": "claude-opus-4-8"},
			wantIntent:      report,
			wantModel:       "claude-opus-4-8",
			wantModelSource: "self_reported",
		},
		{
			name: "the model the client's metadata names wins",
			over: inMemory,
			meta: mcpsdk.Meta{"io.modelcontextprotocol/aiInvocation": map[string]any{"model": "gpt-5.2"}},
			arguments: map[string]any{
				"context": report, "llm_model": "claude-opus-4-8",
			},
			wantIntent:      report,
			wantModel:       "gpt-5.2",
			wantModelSource: "client_metadata",
		},
		{
			name:       "no model is captured when model capture is off",
			over:       inMemory,
			opts:       []Option{WithCaptureModel(false)},
			meta:       mcpsdk.Meta{"io.modelcontextprotocol/aiInvocation": map[string]any{"model": "gpt-5.2"}},
			arguments:  map[string]any{"context": report, "llm_model": "claude-opus-4-8"},
			wantIntent: report,
		},
		{
			name:       "the intent is captured with the context parameter off",
			over:       inMemory,
			opts:       []Option{WithContextParameter(false)},
			arguments:  map[string]any{"context": report},
			wantIntent: report,
		},
		{
			name:       "a custom name",
			over:       inMemory,
			opts:       []Option{WithMissingCapabilityTool("request_tool")},
			call:       "request_tool",
			arguments:  map[string]any{"context": report},
			wantIntent: report,
		},
		{
			name:       "a handle is minted and delivered over stateless HTTP",
			over:       statelessHTTP,
			arguments:  map[string]any{"context": report},
			wantIntent: report,
			wantConversation: func(t *testing.T, delivered, recorded any) {
				assert.Regexp(t, conversationHandle, delivered)
				assert.Equal(t, delivered, recorded)
			},
		},
		{
			name:       "an echoed handle is kept and not delivered again",
			over:       statelessHTTP,
			arguments:  map[string]any{"context": report, "conversation_id": vectorHandle},
			wantIntent: report,
			wantConversation: func(t *testing.T, delivered, recorded any) {
				assert.Empty(t, delivered)
				assert.Equal(t, vectorHandle, recorded)
			},
		},
		{
			name:       "no handle is minted when conversation anchoring is off",
			over:       statelessHTTP,
			opts:       []Option{WithConversationID(false)},
			arguments:  map[string]any{"context": report},
			wantIntent: report,
			wantConversation: func(t *testing.T, delivered, recorded any) {
				assert.Empty(t, delivered)
				assert.Nil(t, recorded)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			var reported []error
			server := newSessionlessServer()
			Instrument(server, posthogmcp.New(queue), append([]Option{
				WithMissingCapabilityTool(""),
				WithServerInfo("test-server", "1.0.0"),
				WithErrorHandler(func(_ context.Context, err error) { reported = append(reported, err) }),
			}, test.opts...)...)
			addWeatherTool(server, nil)
			name := cmp.Or(test.call, "get_more_tools")

			result, err := connect(t, server, test.over).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Meta: test.meta, Arguments: test.arguments})

			require.NoError(t, err)
			require.NotEmpty(t, result.Content)
			text, ok := result.Content[0].(*mcpsdk.TextContent)
			require.True(t, ok)
			assert.Equal(t, cannedMissingCapabilityText, text.Text)
			assert.False(t, result.IsError)
			assert.Equal(t, []string{"$mcp_missing_capability"}, queue.eventNames(), "one report, no tool call, exception, or unknown tool")
			assert.Empty(t, reported)

			properties := queue.captures("$mcp_missing_capability")[0].Properties
			assert.Equal(t, name, properties["$mcp_resource_name"])
			assert.Equal(t, "test-server", properties["$mcp_server_name"])
			assert.NotEmpty(t, properties["$session_id"])
			assert.Equal(t, test.wantIntent, properties["$mcp_intent"])
			assert.Equal(t, test.wantModel, properties["$mcp_llm_model"])
			assert.Equal(t, test.wantModelSource, properties["$mcp_llm_model_source"])
			if test.wantIntent != nil {
				assert.Equal(t, "context_parameter", properties["$mcp_intent_source"])
			} else {
				assert.NotContains(t, properties, "$mcp_intent_source")
			}
			for _, absent := range []string{"$mcp_tool_name", "$mcp_parameters", "$mcp_duration_ms", "$mcp_is_error", "$mcp_response"} {
				assert.NotContains(t, properties, absent)
			}
			if test.wantConversation != nil {
				test.wantConversation(t, deliveredHandle(t, result), properties["$mcp_conversation_id"])
			}
		})
	}
}

func TestMissingCapabilityToolCallSurvivesAFailedListing(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	server.AddReceivingMiddleware(failListings)
	Instrument(server, posthogmcp.New(queue), WithMissingCapabilityTool(""))
	addWeatherTool(server, nil)

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name: "get_more_tools", Arguments: map[string]any{"context": "export a dashboard to PDF"},
	})

	require.NoError(t, err)
	assert.Equal(t, cannedMissingCapabilityText, result.Content[0].(*mcpsdk.TextContent).Text)
	assert.Equal(t, []string{"$mcp_missing_capability"}, queue.eventNames())
}

func TestMissingCapabilityToolCallSurvivesAnalyticsFailure(t *testing.T) {
	server := newServer()
	Instrument(server, posthogmcp.New(&fakeQueue{err: errors.New("queue full")}), WithMissingCapabilityTool(""))
	addWeatherTool(server, nil)

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name: "get_more_tools", Arguments: map[string]any{"context": "export a dashboard to PDF"},
	})

	require.NoError(t, err)
	assert.Equal(t, cannedMissingCapabilityText, result.Content[0].(*mcpsdk.TextContent).Text)
}

func TestMissingCapabilityToolCallIsUnknownUnlessEnabledByName(t *testing.T) {
	for _, test := range []struct {
		name string
		opts []Option
	}{
		{"off by default", nil},
		{"the default name when a custom one is set", []Option{WithMissingCapabilityTool("request_tool")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue), test.opts...)
			addWeatherTool(server, nil)

			_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name: "get_more_tools", Arguments: map[string]any{"context": "export a dashboard to PDF"},
			})

			require.Error(t, err)
			assert.Equal(t, []string{"$mcp_unknown_tool"}, queue.eventNames())
		})
	}
}

func failListings(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method == methodListTools {
			return nil, errors.New("listing unavailable")
		}
		return next(ctx, method, req)
	}
}

func TestMissingCapabilityToolNamedByTheServerIsTheServers(t *testing.T) {
	for _, test := range []struct {
		name       string
		middleware []mcpsdk.Middleware
	}{
		{"the listing is learned", nil},
		{"the listing cannot be learned", []mcpsdk.Middleware{failListings}},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			for _, middleware := range test.middleware {
				server.AddReceivingMiddleware(middleware)
			}
			Instrument(server, posthogmcp.New(queue), WithMissingCapabilityTool(""))
			server.AddTool(&mcpsdk.Tool{Name: "get_more_tools", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "the server's own"}}}, nil
				})

			result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "get_more_tools"})

			require.NoError(t, err)
			assert.Equal(t, "the server's own", result.Content[0].(*mcpsdk.TextContent).Text)
			assert.Equal(t, []string{"$mcp_tool_call"}, queue.eventNames())
		})
	}
}

func TestMissingCapabilityToolCallIsGuardedByInnerMiddleware(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodCallTool {
				return nil, errors.New("unauthenticated")
			}
			return next(ctx, method, req)
		}
	})
	Instrument(server, posthogmcp.New(queue), WithMissingCapabilityTool(""))
	addWeatherTool(server, nil)

	_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name: "get_more_tools", Arguments: map[string]any{"context": "export a dashboard to PDF"},
	})

	require.ErrorContains(t, err, "unauthenticated")
	assert.NotContains(t, queue.eventNames(), "$mcp_missing_capability")
}
