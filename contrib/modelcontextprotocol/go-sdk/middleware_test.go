package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
	posthogmcp "github.com/posthog/posthog-go/mcp"
)

type recordingRecorder struct {
	mu    sync.Mutex
	calls []posthogmcp.ToolCall
	err   error
	panic bool
}

type recorderFunc func(posthogmcp.ToolCall) error

func (f recorderFunc) CaptureToolCall(call posthogmcp.ToolCall) error { return f(call) }

type enqueueRecorder struct {
	mu       sync.Mutex
	messages []posthog.Message
}

func (r *enqueueRecorder) Enqueue(message posthog.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	return nil
}

func (r *enqueueRecorder) snapshot() []posthog.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]posthog.Message(nil), r.messages...)
}

func (r *recordingRecorder) CaptureToolCall(call posthogmcp.ToolCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	if r.panic {
		panic("recorder panic")
	}
	return r.err
}

func (r *recordingRecorder) snapshot() []posthogmcp.ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]posthogmcp.ToolCall(nil), r.calls...)
}

func TestInstrumentCapturesSuccessfulToolCall(t *testing.T) {
	recorder := &recordingRecorder{}
	server := newServer()
	Instrument(server, recorder,
		WithServerInfo("weather-server", "1.2.3"),
		WithIdentity(func(context.Context, *mcpsdk.CallToolRequest) (Identity, error) {
			return Identity{
				DistinctID:    "user-123",
				Groups:        posthog.Groups{"company": "acme"},
				SetProperties: posthog.Properties{"plan": "pro"},
			}, nil
		}),
		WithToolMetadata(func(context.Context, *mcpsdk.CallToolRequest) (ToolMetadata, error) {
			return ToolMetadata{Description: "Get current weather", Category: "weather"}, nil
		}),
		WithProperties(func(
			context.Context,
			*mcpsdk.CallToolRequest,
			*mcpsdk.CallToolResult,
			error,
		) (posthog.Properties, error) {
			return posthog.Properties{"environment": "test"}, nil
		}),
	)

	type input struct {
		City string `json:"city"`
	}
	type output struct {
		Temperature int `json:"temperature"`
	}
	var handlerCalls atomic.Int32
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "weather"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		input,
	) (*mcpsdk.CallToolResult, output, error) {
		handlerCalls.Add(1)
		time.Sleep(time.Millisecond)
		return nil, output{Temperature: 21}, nil
	})

	client := connectInMemory(t, server)
	result, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("CallTool unexpectedly returned a tool error")
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}

	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.ToolName != "weather" || call.ToolDescription != "Get current weather" || call.ToolCategory != "weather" {
		t.Fatalf("tool metadata = %#v", call)
	}
	if call.DistinctID != "user-123" || call.Groups["company"] != "acme" || call.SetProperties["plan"] != "pro" {
		t.Fatalf("identity = %#v", call)
	}
	if call.ServerName != "weather-server" || call.ServerVersion != "1.2.3" {
		t.Fatalf("server info = %q %q", call.ServerName, call.ServerVersion)
	}
	if call.ClientName != "test-client" || call.ClientVersion != "2.0.0" {
		t.Fatalf("client info = %q %q", call.ClientName, call.ClientVersion)
	}
	if call.ProtocolVersion == "" {
		t.Fatal("protocol version was not captured")
	}
	if call.Duration <= 0 || call.Timestamp.IsZero() {
		t.Fatalf("timing = duration %v, timestamp %v", call.Duration, call.Timestamp)
	}
	if call.IsError || call.Error != nil {
		t.Fatalf("error state = %t, %v", call.IsError, call.Error)
	}
	if call.Properties["environment"] != "test" {
		t.Fatalf("properties = %#v", call.Properties)
	}

	var parameters map[string]any
	if err := json.Unmarshal(call.Parameters.(json.RawMessage), &parameters); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if parameters["city"] != "Melbourne" {
		t.Fatalf("parameters = %#v", parameters)
	}
	if _, ok := call.Response.(*mcpsdk.CallToolResult); !ok {
		t.Fatalf("response type = %T", call.Response)
	}
}

func TestInstrumentEndToEndBuildsPostHogCapture(t *testing.T) {
	queue := &enqueueRecorder{}
	analytics := posthogmcp.New(queue, posthogmcp.WithExceptionAutocapture(false))
	server := newServer()
	Instrument(server, analytics,
		WithServerInfo("weather-server", "1.2.3"),
		WithIdentity(func(context.Context, *mcpsdk.CallToolRequest) (Identity, error) {
			return Identity{DistinctID: "user-123"}, nil
		}),
	)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "weather"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"temperature": 21}, nil
	})

	if _, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	messages := queue.snapshot()
	if len(messages) != 1 {
		t.Fatalf("PostHog messages = %d, want 1", len(messages))
	}
	capture, ok := messages[0].(posthog.Capture)
	if !ok {
		t.Fatalf("PostHog message type = %T, want posthog.Capture", messages[0])
	}
	if capture.Event != "$mcp_tool_call" || capture.DistinctId != "user-123" {
		t.Fatalf("capture identity = event %q, distinct ID %q", capture.Event, capture.DistinctId)
	}
	if capture.Properties["$mcp_tool_name"] != "weather" || capture.Properties["$mcp_server_name"] != "weather-server" {
		t.Fatalf("capture properties = %#v", capture.Properties)
	}
	if capture.Properties["$mcp_parameters"] == nil || capture.Properties["$mcp_response"] == nil {
		t.Fatalf("capture payload properties = %#v", capture.Properties)
	}
}

func TestInstrumentClassifiesToolAndProtocolErrors(t *testing.T) {
	t.Run("typed tool error", func(t *testing.T) {
		recorder := &recordingRecorder{}
		server := newServer()
		Instrument(server, recorder)
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "fail"}, func(
			context.Context,
			*mcpsdk.CallToolRequest,
			map[string]any,
		) (*mcpsdk.CallToolResult, any, error) {
			return nil, nil, errors.New("forecast unavailable")
		})

		result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
			Name:      "fail",
			Arguments: map[string]any{},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !result.IsError {
			t.Fatal("typed handler error was not returned as a tool error")
		}

		call := onlyCall(t, recorder)
		if !call.IsError || call.ErrorType != "MCPToolError" {
			t.Fatalf("error state = %t %q", call.IsError, call.ErrorType)
		}
		if call.Error == nil || call.Error.Error() != "forecast unavailable" {
			t.Fatalf("captured error = %v", call.Error)
		}
	})

	t.Run("protocol error", func(t *testing.T) {
		recorder := &recordingRecorder{}
		server := newServer()
		Instrument(server, recorder)
		server.AddTool(
			&mcpsdk.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return nil, errors.New("handler failed")
			},
		)

		_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
			Name:      "fail",
			Arguments: map[string]any{},
		})
		if err == nil || !strings.Contains(err.Error(), "handler failed") {
			t.Fatalf("CallTool error = %v", err)
		}

		call := onlyCall(t, recorder)
		if !call.IsError || call.ErrorType != "MCPProtocolError" {
			t.Fatalf("error state = %t %q", call.IsError, call.ErrorType)
		}
		if call.Error == nil || call.Error.Error() != "handler failed" {
			t.Fatalf("captured error = %v", call.Error)
		}
	})
}

func TestInstrumentPrivacyControlsAndUnrelatedMethods(t *testing.T) {
	recorder := &recordingRecorder{}
	server := newServer()
	Instrument(server, recorder, WithCaptureParameters(false), WithCaptureResponses(false))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"secret": "response"}, nil
	})

	client := connectInMemory(t, server)
	if _, err := client.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatal("unrelated MCP method was captured")
	}
	if _, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"secret": "parameter"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	call := onlyCall(t, recorder)
	if call.Parameters != nil || call.Response != nil {
		t.Fatalf("captured disabled payloads: parameters=%#v response=%#v", call.Parameters, call.Response)
	}
}

func TestInstrumentationFailuresDoNotChangeResponse(t *testing.T) {
	recorderErr := errors.New("queue unavailable")
	recorder := &recordingRecorder{err: recorderErr}
	var mu sync.Mutex
	var reported []string
	server := newServer()
	Instrument(server, recorder,
		WithIdentity(func(context.Context, *mcpsdk.CallToolRequest) (Identity, error) {
			return Identity{}, errors.New("identity unavailable")
		}),
		WithToolMetadata(func(context.Context, *mcpsdk.CallToolRequest) (ToolMetadata, error) {
			panic("metadata panic")
		}),
		WithProperties(func(
			context.Context,
			*mcpsdk.CallToolRequest,
			*mcpsdk.CallToolResult,
			error,
		) (posthog.Properties, error) {
			return posthog.Properties{"later_resolver": true}, nil
		}),
		WithErrorHandler(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err.Error())
		}),
	)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ok"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"ok": true}, nil
	})

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "ok",
		Arguments: map[string]any{},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP response changed: result=%#v err=%v", result, err)
	}
	call := onlyCall(t, recorder)
	if call.Properties["later_resolver"] != true {
		t.Fatalf("later resolver did not run: %#v", call.Properties)
	}

	mu.Lock()
	gotReported := append([]string(nil), reported...)
	mu.Unlock()
	if len(gotReported) != 3 {
		t.Fatalf("reported errors = %#v, want 3", gotReported)
	}
	for _, stage := range []string{"identity resolver", "tool metadata resolver", "recorder"} {
		if !containsString(gotReported, stage) {
			t.Fatalf("reported errors %q do not include stage %q", gotReported, stage)
		}
	}
}

func TestRecorderAndErrorHandlerPanicsDoNotChangeResponse(t *testing.T) {
	recorder := &recordingRecorder{panic: true}
	server := newServer()
	Instrument(server, recorder, WithErrorHandler(func(context.Context, error) {
		panic("error handler panic")
	}))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ok"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"ok": true}, nil
	})

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "ok",
		Arguments: map[string]any{},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP response changed: result=%#v err=%v", result, err)
	}
	_ = onlyCall(t, recorder)
}

func TestMiddlewarePreservesDownstreamPanic(t *testing.T) {
	want := &struct{}{}
	handler := NewMiddleware(&recordingRecorder{})(func(
		context.Context,
		string,
		mcpsdk.Request,
	) (mcpsdk.Result, error) {
		panic(want)
	})

	defer func() {
		if got := recover(); got != want {
			t.Fatalf("recovered panic = %#v, want original panic %#v", got, want)
		}
	}()
	_, _ = handler(t.Context(), methodCallTool, nil)
}

func TestMiddlewareOrdering(t *testing.T) {
	var mu sync.Mutex
	var order []string
	appendOrder := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}

	server := newServer()
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodCallTool {
				appendOrder("inner-before")
				defer appendOrder("inner-after")
			}
			return next(ctx, method, req)
		}
	})
	Instrument(server, recorderFunc(func(posthogmcp.ToolCall) error {
		appendOrder("recorder")
		return nil
	}))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ok"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		appendOrder("handler")
		return nil, map[string]any{"ok": true}, nil
	})

	if _, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "ok",
		Arguments: map[string]any{},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if want := "inner-before,handler,inner-after,recorder"; got != want {
		t.Fatalf("middleware order = %q, want %q", got, want)
	}
}

func TestStreamableHTTPMapsSessionID(t *testing.T) {
	recorder := &recordingRecorder{}
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"},
		&mcpsdk.ServerOptions{GetSessionID: func() string { return "session-123" }},
	)
	Instrument(server, recorder)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ok"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"ok": true}, nil
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "ok",
		Arguments: map[string]any{},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := onlyCall(t, recorder).SessionID; got != "session-123" {
		t.Fatalf("session ID = %q, want session-123", got)
	}
}

func newServer() *mcpsdk.Server {
	return mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
}

func connectInMemory(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "2.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func onlyCall(t *testing.T, recorder *recordingRecorder) posthogmcp.ToolCall {
	t.Helper()
	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(calls))
	}
	return calls[0]
}

func containsString(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}
