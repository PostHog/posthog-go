package posthogmcpgo

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/posthog/posthog-go/posthogmcpsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func advertisedSchema(t *testing.T, tool mcp.Tool) (properties map[string]any, required []string) {
	t.Helper()
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	require.NoError(t, json.Unmarshal([]byte(jsonString(t, tool)), &struct {
		InputSchema any `json:"inputSchema"`
	}{&schema}))
	return schema.Properties, schema.Required
}

// TestInstrumentKeepsValidatingServersAcceptingAdvertisedCalls sends each
// call shaped by the advertised schema, as an agent would, to a server that
// validates input against the registered schema before any middleware.
func TestInstrumentKeepsValidatingServersAcceptingAdvertisedCalls(t *testing.T) {
	for _, test := range []struct {
		name         string
		schema       string
		wantInjected bool
	}{
		{name: "an open schema", schema: `{"type":"object","properties":{"n":{"type":"integer"}}}`, wantInjected: true},
		{name: "additionalProperties true", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":true}`, wantInjected: true},
		{name: "annotations", schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"n","type":"object","properties":{"n":{"type":"integer"}},"$defs":{"x":{}}}`, wantInjected: true},
		{name: "a ref that is not the root", schema: `{"type":"object","properties":{"n":{"$ref":"#/$defs/count"}},"$defs":{"count":{"type":"integer"}}}`, wantInjected: true},
		{name: "minProperties", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"minProperties":1}`},
		{name: "a recursive property", schema: `{"type":"object","properties":{"n":{"type":"integer"},"child":{"$ref":"#"}}}`},
		{name: "a recursive definition", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"$defs":{"tree":{"type":"object","properties":{"kids":{"type":"array","items":{"$ref":"#"}}}}}}`},
		{name: "no properties", schema: `{"type":"object"}`, wantInjected: true},
		{name: "additionalProperties false", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`},
		{name: "additionalProperties schema", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":{"type":"integer"}}`},
		{name: "unevaluatedProperties false", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"unevaluatedProperties":false}`},
		{name: "maxProperties", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"maxProperties":1}`},
		{name: "propertyNames", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"propertyNames":{"pattern":"^[a-z]$"}}`},
		{name: "patternProperties", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"patternProperties":{".*":{"type":"integer"}}}`},
		{name: "not", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"not":{"required":["context"]}}`},
		{name: "if", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"if":{"required":["context"]},"then":false}`},
		{name: "dependentRequired", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"dependentRequired":{"context":["missing"]}}`},
		{name: "allOf", schema: `{"type":"object","allOf":[{"properties":{"n":{"type":"integer"}}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			handler := &weatherHandler{}
			s := server.NewMCPServer("weather-server", "1.2.3",
				server.WithToolCapabilities(true),
				server.WithInputSchemaValidation(),
				Instrument(posthogmcp.New(queue)),
			)
			s.AddTool(mcp.NewToolWithRawSchema("count", "Count", json.RawMessage(test.schema)), handler.handle)
			c := connectInProcess(t, s)

			listed, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
			require.NoError(t, err)
			properties, required := advertisedSchema(t, listed.Tools[0])
			arguments := map[string]any{"n": 1}
			for _, name := range required {
				if name == contextArgument || name == modelArgument {
					arguments[name] = "Counting items for a report"
				}
			}
			assert.Equal(t, test.wantInjected, properties[contextArgument] != nil, "context advertised")

			result, err := callTool(t, c, "count", arguments)
			require.NoError(t, err)
			assert.False(t, result.IsError, "the server accepts the advertised call: %s", toolResultText(result))
			require.Len(t, handler.received, 1)
			assert.Equal(t, map[string]any{"n": float64(1)}, handler.received[0])
			assert.Equal(t, false, queue.onlyToolCall(t)["$mcp_is_error"])
		})
	}
}

func TestInstrumentKeepsLargeIntegersInSchemas(t *testing.T) {
	schema := `{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}}}`
	s := server.NewMCPServer("weather-server", "1.2.3", server.WithToolCapabilities(true), Instrument(posthogmcp.New(&fakeQueue{})))
	s.AddTool(mcp.NewToolWithRawSchema("count", "Count", json.RawMessage(schema)), (&weatherHandler{}).handle)

	listed := s.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))

	assert.Contains(t, jsonString(t, listed), `"const":9007199254740993`)
	assert.Contains(t, jsonString(t, listed), `"context"`, "the schema was extended")
}

// TestInstrumentCapturesWhatTheClientReceives checks the event against the
// response mcp-go sends, including failures decided outside the handler.
func TestInstrumentCapturesWhatTheClientReceives(t *testing.T) {
	structured := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultStructured(map[string]any{"temp": "hot"}, "hot"), nil
	}
	for _, test := range []struct {
		name       string
		options    []server.ServerOption
		tool       mcp.Tool
		handler    server.ToolHandlerFunc
		call       string
		arguments  map[string]any
		wantEvents int
	}{
		{
			name:       "input that fails validation",
			options:    []server.ServerOption{server.WithInputSchemaValidation()},
			tool:       weatherTool(),
			handler:    (&weatherHandler{}).handle,
			call:       "weather",
			arguments:  map[string]any{"city": 42, "context": "Checking a forecast"},
			wantEvents: 1,
		},
		{
			name:       "output that fails validation",
			options:    []server.ServerOption{server.WithOutputSchemaValidation()},
			tool:       mcp.NewTool("weather", mcp.WithRawOutputSchema(json.RawMessage(`{"type":"object","properties":{"temp":{"type":"number"}},"required":["temp"]}`))),
			handler:    structured,
			call:       "weather",
			arguments:  map[string]any{"context": "Checking a forecast"},
			wantEvents: 1,
		},
		{
			name:      "an unknown tool is not a tool call",
			tool:      weatherTool(),
			handler:   (&weatherHandler{}).handle,
			call:      "forecast",
			arguments: map[string]any{"city": "Paris"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			s := server.NewMCPServer("weather-server", "1.2.3",
				append(append([]server.ServerOption{server.WithToolCapabilities(true)}, test.options...), Instrument(posthogmcp.New(queue)))...)
			s.AddTool(test.tool, test.handler)
			c := connectInProcess(t, s)

			result, err := callTool(t, c, test.call, test.arguments)

			captures := queue.toolCalls()
			require.Len(t, captures, test.wantEvents)
			if test.wantEvents == 0 {
				assert.Error(t, err)
				assert.Empty(t, queue.exceptions())
				return
			}
			require.NoError(t, err)
			require.True(t, result.IsError)
			assert.Equal(t, true, captures[0].Properties["$mcp_is_error"])
			assert.Equal(t, toolResultText(result), captures[0].Properties["$mcp_error_message"])
			assert.Equal(t, "Checking a forecast", captures[0].Properties["$mcp_intent"])
			assert.Len(t, queue.exceptions(), 1)
		})
	}
}

// TestInstrumentWithTaskAugmentedCalls covers tools/calls run as tasks, which
// mcp-go answers with the task rather than the tool's result.
func TestInstrumentWithTaskAugmentedCalls(t *testing.T) {
	arguments := map[string]any{"city": "Paris", "context": "Planning a trip", "llm_model": "claude-opus-4-8"}
	for _, test := range []struct {
		name           string
		register       func(s *server.MCPServer, received chan<- map[string]any)
		wantAdvertised bool
		wantReceived   map[string]any
	}{
		{
			name: "a task tool, which runs no middleware, is left unchanged and not captured",
			register: func(s *server.MCPServer, received chan<- map[string]any) {
				s.AddTaskTool(mcp.NewTool("forecast", mcp.WithString("city"), mcp.WithTaskSupport(mcp.TaskSupportRequired)),
					func(_ context.Context, req mcp.CallToolRequest) (*mcp.CreateTaskResult, error) {
						received <- req.GetArguments()
						return &mcp.CreateTaskResult{}, nil
					})
			},
			wantReceived: arguments,
		},
		{
			name: "a regular tool run as a task is stripped and not captured, since its outcome is not observed",
			register: func(s *server.MCPServer, received chan<- map[string]any) {
				s.AddTool(mcp.NewTool("forecast", mcp.WithString("city"), mcp.WithTaskSupport(mcp.TaskSupportOptional)),
					func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						received <- req.GetArguments()
						return mcp.NewToolResultText("Sunny"), nil
					})
			},
			wantAdvertised: true,
			wantReceived:   map[string]any{"city": "Paris"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			received := make(chan map[string]any, 1)
			s := server.NewMCPServer("weather-server", "1.2.3",
				server.WithToolCapabilities(true),
				server.WithTaskCapabilities(true, true, true),
				Instrument(posthogmcp.New(queue)),
			)
			test.register(s, received)
			c := connectInProcess(t, s)

			listed, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
			require.NoError(t, err)
			properties, _ := advertisedSchema(t, listed.Tools[0])
			assert.Equal(t, test.wantAdvertised, properties[contextArgument] != nil, "context advertised")

			req := mcp.CallToolRequest{}
			req.Params.Name = "forecast"
			req.Params.Arguments = arguments
			task := mcp.NewTaskParams(nil)
			req.Params.Task = &task
			_, _ = c.CallTool(t.Context(), req)

			select {
			case got := <-received:
				assert.Equal(t, test.wantReceived, got)
			case <-time.After(5 * time.Second):
				t.Fatal("the handler never ran")
			}
			assert.Never(t, func() bool { return len(queue.toolCalls()) > 0 }, 200*time.Millisecond, 10*time.Millisecond)
		})
	}
}

// TestInstrumentKeepsTheHostsHooks registers a host's session tool from its own
// hooks, before and after Instrument, and calls it twice.
func TestInstrumentKeepsTheHostsHooks(t *testing.T) {
	for _, test := range []struct {
		name         string
		hooksFirst   bool
		wantReported int
	}{
		{name: "hooks given before Instrument", hooksFirst: true},
		{name: "hooks given after Instrument replace its hooks, and the middleware captures instead", wantReported: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			var reportedMu sync.Mutex
			var reported []error
			received := make(chan map[string]any, 2)
			var s *server.MCPServer
			hooks := &server.Hooks{}
			hooks.AddOnRegisterSession(func(_ context.Context, session server.ClientSession) {
				_ = s.AddSessionTool(session.SessionID(), mcp.NewTool("mine", mcp.WithString("q")),
					func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						received <- req.GetArguments()
						return mcp.NewToolResultText("ok"), nil
					})
			})
			instrument := Instrument(posthogmcp.New(queue), WithErrorHandler(func(_ context.Context, err error) {
				reportedMu.Lock()
				defer reportedMu.Unlock()
				reported = append(reported, err)
			}))
			options := []server.ServerOption{server.WithToolCapabilities(true), instrument, server.WithHooks(hooks)}
			if test.hooksFirst {
				options = []server.ServerOption{server.WithToolCapabilities(true), server.WithHooks(hooks), instrument}
			}
			s = server.NewMCPServer("weather-server", "1.2.3", options...)
			c := connectHTTP()(t, s)

			for range 2 {
				_, err := callTool(t, c, "mine", map[string]any{"q": "1", "context": "Looking something up", "llm_model": "gpt-5.2"})
				require.NoError(t, err)
				assert.Equal(t, map[string]any{"q": "1"}, <-received)
			}

			captures := queue.toolCalls()
			require.Len(t, captures, 2)
			assert.Equal(t, "Looking something up", captures[0].Properties["$mcp_intent"])
			assert.Equal(t, "gpt-5.2", captures[0].Properties["$mcp_llm_model"])
			reportedMu.Lock()
			defer reportedMu.Unlock()
			assert.Len(t, reported, test.wantReported)
		})
	}
}

// TestInstrumentLeavesTypedSchemasForLaterFilters checks that a host tool
// filter that reads a tool's typed input schema works on either side of
// Instrument.
func TestInstrumentLeavesTypedSchemasForLaterFilters(t *testing.T) {
	hideAdmin := server.WithToolFilter(func(_ context.Context, tools []mcp.Tool) []mcp.Tool {
		var kept []mcp.Tool
		for _, tool := range tools {
			if _, admin := tool.InputSchema.Properties["admin_key"]; !admin {
				kept = append(kept, tool)
			}
		}
		return kept
	})
	for _, test := range []struct {
		name    string
		options func(instrument server.ServerOption) []server.ServerOption
	}{
		{name: "host filter first", options: func(instrument server.ServerOption) []server.ServerOption {
			return []server.ServerOption{hideAdmin, instrument}
		}},
		{name: "Instrument first", options: func(instrument server.ServerOption) []server.ServerOption {
			return []server.ServerOption{instrument, hideAdmin}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := server.NewMCPServer("weather-server", "1.2.3",
				append([]server.ServerOption{server.WithToolCapabilities(true)}, test.options(Instrument(posthogmcp.New(&fakeQueue{})))...)...)
			s.AddTool(mcp.NewTool("wipe", mcp.WithString("admin_key", mcp.Required())), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("wiped"), nil
			})
			s.AddTool(weatherTool(), (&weatherHandler{}).handle)
			c := connectInProcess(t, s)

			listed, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
			require.NoError(t, err)
			require.Len(t, listed.Tools, 1)
			assert.Equal(t, "weather", listed.Tools[0].Name)
			assert.Contains(t, listed.Tools[0].InputSchema.Properties, contextArgument, "the typed schema carries the added arguments")
			assert.Empty(t, listed.Tools[0].RawInputSchema)
			_, err = callTool(t, c, "wipe", map[string]any{"admin_key": "x"})
			assert.Error(t, err, "the host's filter blocks the call")
		})
	}
}

func TestInstrumentDoesNotChangeTheRegisteredTool(t *testing.T) {
	s := newInstrumentedServer(&fakeQueue{}, weatherTool(), &weatherHandler{})
	c := connectInProcess(t, s)

	_, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	require.NoError(t, err)

	registered := s.GetTool("weather").Tool
	assert.Equal(t, []string{"city"}, keys(registered.InputSchema.Properties))
	assert.Equal(t, []string{"city"}, registered.InputSchema.Required)
}

func TestAbsentArgumentsMatchPosthogmcpsdk(t *testing.T) {
	sdkQueue := &fakeQueue{}
	sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "ping-server", Version: "1.0.0"}, nil)
	posthogmcpsdk.Instrument(sdkServer, posthogmcp.New(sdkQueue))
	sdkServer.AddTool(&mcpsdk.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "pong"}}}, nil
	})
	_, err := connectGoSDK(t, sdkServer).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "ping"})
	require.NoError(t, err)

	queue := &fakeQueue{}
	s := server.NewMCPServer("ping-server", "1.0.0", server.WithToolCapabilities(true), Instrument(posthogmcp.New(queue)))
	s.AddTool(mcp.NewTool("ping"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("pong"), nil
	})
	_, err = connectInProcess(t, s).CallTool(t.Context(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "ping"}})
	require.NoError(t, err)

	assert.JSONEq(t, jsonString(t, sdkQueue.onlyToolCall(t)["$mcp_parameters"]), jsonString(t, queue.onlyToolCall(t)["$mcp_parameters"]))
}

func TestProtocolVersionPerTransport(t *testing.T) {
	for _, test := range []struct {
		name    string
		connect func(t *testing.T, s *server.MCPServer) *client.Client
	}{
		{name: "in-process", connect: connectInProcess},
		{name: "SSE", connect: connectSSE},
		{name: "streamable HTTP", connect: connectHTTP()},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			c := test.connect(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{}))

			_, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
			require.NoError(t, err)

			assert.Equal(t, mcp.LATEST_PROTOCOL_VERSION, queue.onlyToolCall(t)["$mcp_protocol_version"])
		})
	}
}

func connectSSE(t *testing.T, s *server.MCPServer) *client.Client {
	t.Helper()
	sseServer := server.NewTestServer(s)
	t.Cleanup(sseServer.Close)
	c, err := client.NewSSEMCPClient(sseServer.URL + "/sse")
	require.NoError(t, err)
	return initialize(t, c)
}

func TestInstrumentCapturesOnlyItsOwnServersCalls(t *testing.T) {
	hooks := &server.Hooks{}
	newServer := func(queue *fakeQueue) *server.MCPServer {
		opts := []server.ServerOption{server.WithToolCapabilities(true), server.WithHooks(hooks)}
		if queue != nil {
			opts = append(opts, Instrument(posthogmcp.New(queue)))
		}
		s := server.NewMCPServer("weather-server", "1.2.3", opts...)
		s.AddTool(weatherTool(), (&weatherHandler{}).handle)
		return s
	}
	first, second := &fakeQueue{}, &fakeQueue{}
	instrumented := newServer(first)
	_ = newServer(second)
	uninstrumented := newServer(nil)

	_, err := callTool(t, connectInProcess(t, instrumented), "weather", map[string]any{"city": "Paris"})
	require.NoError(t, err)
	_, err = callTool(t, connectInProcess(t, uninstrumented), "weather", map[string]any{"city": "Lisbon"})
	require.NoError(t, err)

	assert.Len(t, first.toolCalls(), 1, "the instrumented server's call, once")
	assert.Empty(t, second.toolCalls(), "another server sharing the hooks")
}

func TestInstrumentIgnoresSynchronousCallsToTaskTools(t *testing.T) {
	for _, support := range []mcp.TaskSupport{mcp.TaskSupportOptional, mcp.TaskSupportRequired} {
		t.Run(string(support), func(t *testing.T) {
			queue := &fakeQueue{}
			s := server.NewMCPServer("weather-server", "1.2.3",
				server.WithToolCapabilities(true), server.WithTaskCapabilities(true, true, true), Instrument(posthogmcp.New(queue)))
			s.AddTaskTool(mcp.NewTool("forecast", mcp.WithString("city"), mcp.WithTaskSupport(support)),
				func(context.Context, mcp.CallToolRequest) (*mcp.CreateTaskResult, error) {
					return &mcp.CreateTaskResult{}, nil
				})

			_, _ = callTool(t, connectInProcess(t, s), "forecast", map[string]any{"city": "Paris"})

			assert.Empty(t, queue.toolCalls())
			assert.Empty(t, queue.exceptions())
		})
	}
}

func TestInstrumentKeepsTheProtocolVersionWhenTheSessionClosesMidCall(t *testing.T) {
	queue := &fakeQueue{}
	s := server.NewMCPServer("weather-server", "1.2.3", server.WithToolCapabilities(true), Instrument(posthogmcp.New(queue)))
	started, release := make(chan struct{}), make(chan struct{})
	s.AddTool(mcp.NewTool("slow"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		<-release
		return mcp.NewToolResultText("done"), nil
	})
	c := connectInProcess(t, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = callTool(t, c, "slow", nil)
	}()
	<-started
	_ = c.Close()
	close(release)
	<-done

	require.Len(t, queue.toolCalls(), 1)
	assert.Equal(t, mcp.LATEST_PROTOCOL_VERSION, queue.toolCalls()[0].Properties["$mcp_protocol_version"])
}

func TestSessionResolverForgetsClosedSessions(t *testing.T) {
	resolver := newSessionResolver()
	session := server.NewInProcessSession("closed", nil)
	now := time.Now()
	first := resolver.resolve(session, now)

	resolver.forget(session)

	assert.Empty(t, resolver.generated)
	assert.NotEqual(t, first, resolver.resolve(session, now), "a new session id after the session closed")
}

// countingSession is a session with its own tools, which counts how often
// they are copied.
type countingSession struct {
	*server.InProcessSession
	copies *atomic.Int32
}

func (s countingSession) GetSessionTools() map[string]server.ServerTool {
	s.copies.Add(1)
	return map[string]server.ServerTool{}
}

func (s countingSession) SetSessionTools(map[string]server.ServerTool) {}

func TestInstrumentCopiesSessionToolsOncePerListing(t *testing.T) {
	copiesFor := func(toolCount int) int32 {
		s := server.NewMCPServer("weather-server", "1.2.3", server.WithToolCapabilities(true), Instrument(posthogmcp.New(&fakeQueue{})))
		for i := range toolCount {
			s.AddTool(mcp.NewTool(fmt.Sprintf("tool_%d", i)), (&weatherHandler{}).handle)
		}
		copies := &atomic.Int32{}
		session := countingSession{server.NewInProcessSession("counted", nil), copies}
		require.NoError(t, s.RegisterSession(t.Context(), session))
		copies.Store(0)

		listed := s.HandleMessage(s.WithContext(t.Context(), session), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		assert.Contains(t, jsonString(t, listed), `"context"`)
		return copies.Load()
	}

	assert.Equal(t, copiesFor(1), copiesFor(30), "copies do not grow with the number of listed tools")
}
