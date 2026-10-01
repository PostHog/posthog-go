package posthogmcpsdk

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	legacyProtocol = "2025-11-25"
	latestProtocol = "2026-07-28"
)

func (q *fakeQueue) captures(event string) []posthog.Capture {
	q.mu.Lock()
	defer q.mu.Unlock()
	var captures []posthog.Capture
	for _, message := range q.messages {
		if capture, ok := message.(posthog.Capture); ok && capture.Event == event {
			captures = append(captures, capture)
		}
	}
	return captures
}

func (q *fakeQueue) eventNames() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var names []string
	for _, message := range q.messages {
		switch message := message.(type) {
		case posthog.Capture:
			names = append(names, message.Event)
		case posthog.Exception:
			names = append(names, "$exception")
		}
	}
	return names
}

// The multi round-trip API exists from go-sdk v1.8, and the adapter builds
// against v1.6.1, so tests reach it by name and skip on an older go-sdk.

// inputRequired is the result of a round that asks for requests, keyed by id.
func inputRequired(t *testing.T, requests map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	result := &mcpsdk.CallToolResult{}
	field := reflect.ValueOf(result).Elem().FieldByName("InputRequests")
	if !field.IsValid() {
		t.Skip("this go-sdk has no multi round-trip results")
	}
	field.Set(reflect.MakeMap(field.Type()))
	for id, request := range requests {
		field.SetMapIndex(reflect.ValueOf(id), reflect.ValueOf(request))
	}
	return result
}

type clientOptions struct {
	protocol string
	// manualRetry returns each round to the caller instead of retrying it.
	manualRetry bool
}

func connectClient(t *testing.T, server *mcpsdk.Server, opts clientOptions) *mcpsdk.ClientSession {
	t.Helper()
	clientOpts := &mcpsdk.ClientOptions{
		ElicitationHandler: func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			return &mcpsdk.ElicitResult{Action: "accept"}, nil
		},
	}
	if opts.manualRetry {
		options := reflect.ValueOf(clientOpts).Elem().FieldByName("MultiRoundTrip")
		if !options.IsValid() {
			t.Skip("this go-sdk has no multi round-trip client")
		}
		options.Set(reflect.New(options.Type().Elem()))
		options.Elem().FieldByName("Disabled").SetBool(true)
	}
	sessionOpts := &mcpsdk.ClientSessionOptions{}
	if opts.protocol != "" {
		version := reflect.ValueOf(sessionOpts).Elem().FieldByName("ProtocolVersion")
		if !version.IsValid() {
			t.Skip("this go-sdk cannot pick the client protocol version")
		}
		version.SetString(opts.protocol)
	}

	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "2.0.0"}, clientOpts).
		Connect(t.Context(), clientTransport, sessionOpts)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return session
}

// addAskingTool adds a tool whose first round asks for requests and whose
// retry completes.
func addAskingTool(t *testing.T, server *mcpsdk.Server, requests map[string]any) {
	t.Helper()
	round := inputRequired(t, requests)
	var calls atomic.Int32
	server.AddTool(
		&mcpsdk.Tool{Name: "deploy", Description: "Deploy a build", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			if calls.Add(1) == 1 {
				return round, nil
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "deployed"}}}, nil
		},
	)
}

func TestInstrumentInputRequiredRounds(t *testing.T) {
	for _, test := range []struct {
		name        string
		client      clientOptions
		requests    map[string]any
		wantEvents  []string
		wantMethods []any
	}{
		{
			name:   "a round is not a call and its retry is",
			client: clientOptions{protocol: latestProtocol},
			requests: map[string]any{
				"c": &mcpsdk.ElicitParams{Message: "Deploy to production?"},
				"a": &mcpsdk.ElicitParams{Message: "Which region?"},
				"b": &mcpsdk.ListRootsParams{},
			},
			wantEvents:  []string{"$mcp_input_required", "$mcp_tool_call"},
			wantMethods: []any{"elicitation/create", "roots/list", "elicitation/create"},
		},
		{
			name:        "a round with an empty request map has no methods",
			client:      clientOptions{protocol: latestProtocol},
			requests:    map[string]any{},
			wantEvents:  []string{"$mcp_input_required"},
			wantMethods: []any{},
		},
		{
			name:   "a round the legacy shim fulfils is one call",
			client: clientOptions{protocol: legacyProtocol},
			requests: map[string]any{
				"confirm": &mcpsdk.ElicitParams{Message: "Deploy to production?"},
			},
			wantEvents: []string{"$mcp_tool_call"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"))
			addAskingTool(t, server, test.requests)
			test.client.manualRetry = len(test.requests) == 0

			result, err := connectClient(t, server, test.client).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "deploy"})
			require.NoError(t, err)
			assert.Equal(t, len(test.requests) == 0, awaitsInput(result), "the client received the round")

			assert.Equal(t, test.wantEvents, queue.eventNames())
			rounds := queue.captures("$mcp_input_required")
			if test.wantMethods == nil {
				return
			}
			require.Len(t, rounds, 1)
			properties := rounds[0].Properties
			assert.Equal(t, "deploy", properties["$mcp_tool_name"])
			assert.IsType(t, float64(0), properties["$mcp_duration_ms"])
			assert.Equal(t, test.wantMethods, toAnySlice(properties["$mcp_input_request_methods"]))
			assert.Equal(t, "test-server", properties["$mcp_server_name"])
			assert.Equal(t, "test-client", properties["$mcp_client_name"])
			assert.Equal(t, latestProtocol, properties["$mcp_protocol_version"])
			assert.NotEmpty(t, properties["$session_id"])
			assert.NotContains(t, properties, "$mcp_parameters")
			assert.NotContains(t, properties, "$mcp_response")
			assert.NotContains(t, properties, "$mcp_is_error")
		})
	}
}

func TestInstrumentInputRequiredRoundWithUnreadableRequestsSendsNothing(t *testing.T) {
	queue := &fakeQueue{}
	var mu sync.Mutex
	var reported []error
	server := newServer()
	Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"),
		WithErrorHandler(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err)
		}))
	addAskingTool(t, server, map[string]any{"ask": &mcpsdk.ElicitParams{Message: "Deploy?", RequestedSchema: make(chan int)}})

	// go-sdk cannot encode the round either, so the client never gets a reply.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, _ = connectClient(t, server, clientOptions{protocol: latestProtocol}).CallTool(ctx, &mcpsdk.CallToolParams{Name: "deploy"})

	assert.Empty(t, queue.eventNames())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, reported, 1)
	assert.Contains(t, reported[0].Error(), "posthogmcpsdk: capture: input request methods: ")
}

// go-sdk fulfils a legacy client's requests once and runs the handler again. A
// handler that asks again ends the call with a result that still asks.
func TestInstrumentLegacyShimExhaustionIsAFailedCall(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"))
	var runs atomic.Int32
	server.AddTool(
		&mcpsdk.Tool{Name: "deploy", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			runs.Add(1)
			return inputRequired(t, map[string]any{"confirm": &mcpsdk.ElicitParams{Message: "Again?"}}), nil
		},
	)

	_, _ = connectClient(t, server, clientOptions{protocol: legacyProtocol, manualRetry: true}).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "deploy"})

	assert.Equal(t, int32(2), runs.Load())
	assert.Equal(t, []string{"$mcp_tool_call", "$exception"}, queue.eventNames())
	properties := queue.onlyToolCall(t)
	assert.Equal(t, true, properties["$mcp_is_error"])
	assert.Equal(t, "input_required", properties["$mcp_error_type"])
}

func toAnySlice(value any) []any {
	switch value := value.(type) {
	case []string:
		converted := make([]any, len(value))
		for i, item := range value {
			converted[i] = item
		}
		return converted
	case []any:
		return value
	}
	return nil
}

// A handle minted for a round never reached the agent, so no event names it,
// and the retry that completes the call gets its own.
func TestInstrumentInputRequiredRoundDoesNotRecordAnUnreceivedHandle(t *testing.T) {
	queue := &fakeQueue{}
	server := newSessionlessServer()
	Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"))
	addAskingTool(t, server, map[string]any{"roots": &mcpsdk.ListRootsParams{}})
	session := connect(t, server, statelessHTTP)

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "deploy"})
	require.NoError(t, err)

	rounds := queue.captures("$mcp_input_required")
	require.Len(t, rounds, 1)
	assert.NotContains(t, rounds[0].Properties, "$mcp_conversation_id")
	handle := deliveredHandle(t, result)
	require.Regexp(t, conversationHandle, handle)
	assert.Equal(t, handle, queue.onlyToolCall(t)["$mcp_conversation_id"])
}

func TestInstrumentUnknownTool(t *testing.T) {
	for _, test := range []struct {
		name             string
		over             transport
		call             string
		arguments        any
		wantEvents       []string
		wantTool         string
		wantConversation any
		wantSessionID    any
	}{
		{
			name:       "an unregistered name",
			over:       inMemory,
			call:       "delete_everything",
			arguments:  map[string]any{},
			wantEvents: []string{"$mcp_unknown_tool"},
			wantTool:   "delete_everything",
		},
		{
			name:       "an unregistered name over stateless HTTP records no unreceived handle",
			over:       statelessHTTP,
			call:       "delete_everything",
			arguments:  map[string]any{},
			wantEvents: []string{"$mcp_unknown_tool"},
			wantTool:   "delete_everything",
		},
		{
			name:             "an echoed handle is the conversation of an unregistered name",
			over:             statelessHTTP,
			call:             "delete_everything",
			arguments:        map[string]any{"conversation_id": vectorHandle},
			wantEvents:       []string{"$mcp_unknown_tool"},
			wantTool:         "delete_everything",
			wantConversation: vectorHandle,
			wantSessionID:    vectorSessionID,
		},
		{
			name:             "an upper-case echoed handle is lower-cased",
			over:             statelessHTTP,
			call:             "delete_everything",
			arguments:        map[string]any{"conversation_id": "0190F0E8-7A6B-7C3D-9E4F-5A6B7C8D9E0F"},
			wantEvents:       []string{"$mcp_unknown_tool"},
			wantTool:         "delete_everything",
			wantConversation: vectorHandle,
			wantSessionID:    vectorSessionID,
		},
		{
			name:       "an invented handle is not a conversation",
			over:       statelessHTTP,
			call:       "delete_everything",
			arguments:  map[string]any{"conversation_id": "not-a-uuid"},
			wantEvents: []string{"$mcp_unknown_tool"},
			wantTool:   "delete_everything",
		},
		{
			name:       "a call naming no tool",
			over:       inMemory,
			call:       "",
			arguments:  map[string]any{},
			wantEvents: nil,
		},
		{
			name:       "a call naming only whitespace",
			over:       inMemory,
			call:       "  \t",
			arguments:  map[string]any{},
			wantEvents: nil,
		},
		{
			name:       "invalid arguments for a registered tool",
			over:       inMemory,
			call:       "weather",
			arguments:  map[string]any{"city": 42},
			wantEvents: []string{"$mcp_tool_call", "$exception"},
			wantTool:   "weather",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			var reported []error
			server := newSessionlessServer()
			Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"),
				WithErrorHandler(func(_ context.Context, err error) { reported = append(reported, err) }))
			addWeatherTool(server, nil)

			_, _ = connect(t, server, test.over).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: test.call, Arguments: test.arguments})

			assert.Equal(t, test.wantEvents, queue.eventNames())
			assert.Empty(t, reported)
			if len(test.wantEvents) == 0 || test.wantEvents[0] != "$mcp_unknown_tool" {
				return
			}
			properties := queue.captures("$mcp_unknown_tool")[0].Properties
			assert.Equal(t, test.wantTool, properties["$mcp_tool_name"])
			assert.Equal(t, "test-server", properties["$mcp_server_name"])
			if test.over == inMemory {
				assert.Equal(t, "test-client", properties["$mcp_client_name"])
			}
			assert.NotEmpty(t, properties["$session_id"])
			assert.Equal(t, test.wantConversation, properties["$mcp_conversation_id"])
			if test.wantSessionID != nil {
				assert.Equal(t, test.wantSessionID, properties["$session_id"])
			}
		})
	}
}

// The catalog can be stale in either direction on a stateless server, which
// sends no list_changed. Only go-sdk's own unknown tool error says a name is
// not registered.
func TestInstrumentClassifiesUnknownToolsByTheFrameworksError(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(server *mcpsdk.Server)
		call       string
		wantEvents []string
	}{
		{
			name: "a tool added after the last listing whose handler fails",
			change: func(server *mcpsdk.Server) {
				server.AddTool(&mcpsdk.Tool{Name: "fresh", InputSchema: map[string]any{"type": "object"}},
					func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
						return nil, errors.New("boom")
					})
			},
			call:       "fresh",
			wantEvents: []string{"$mcp_tool_call", "$mcp_tool_call", "$exception"},
		},
		{
			name: "a tool forwarding another tool's unknown tool error",
			change: func(server *mcpsdk.Server) {
				server.AddTool(&mcpsdk.Tool{Name: "proxy", InputSchema: map[string]any{"type": "object"}},
					func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
						return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `unknown tool "upstream"`}
					})
			},
			call:       "proxy",
			wantEvents: []string{"$mcp_tool_call", "$mcp_tool_call", "$exception"},
		},
		{
			name:       "a tool removed after the last listing",
			change:     func(server *mcpsdk.Server) { server.RemoveTools("weather") },
			call:       "weather",
			wantEvents: []string{"$mcp_tool_call", "$mcp_unknown_tool"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newSessionlessServer()
			Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"))
			addWeatherTool(server, nil)
			session := connect(t, server, statelessHTTP)
			_, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "weather", Arguments: map[string]any{"city": "Melbourne"}})
			require.NoError(t, err)

			test.change(server)
			_, _ = session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: test.call, Arguments: map[string]any{"city": "Melbourne"}})

			assert.Equal(t, test.wantEvents, queue.eventNames())
		})
	}
}

// A result that asks for input next to an error is not a round the client
// receives, so the call is a failure like any other.
func TestInstrumentRecordsAFailedCallWhenARoundComesWithAnError(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if method == "tools/call" && err == nil {
				return result, errors.New("boom")
			}
			return result, err
		}
	})
	Instrument(server, posthogmcp.New(queue), WithServerInfo("test-server", "1.0.0"))
	addAskingTool(t, server, map[string]any{"ask": &mcpsdk.ElicitParams{Message: "Deploy?"}})

	_, _ = connectClient(t, server, clientOptions{protocol: latestProtocol}).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "deploy"})

	assert.Equal(t, []string{"$mcp_tool_call", "$exception"}, queue.eventNames())
}
