package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeQueue struct {
	mu       sync.Mutex
	messages []posthog.Message
	err      error
	panic    bool
}

func (q *fakeQueue) Enqueue(message posthog.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
	if q.panic {
		panic("enqueue panic")
	}
	return q.err
}

func (q *fakeQueue) toolCalls(t *testing.T) []posthog.Capture {
	t.Helper()
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
	captures := q.toolCalls(t)
	require.Len(t, captures, 1)
	return captures[0].Properties
}

type weatherInput struct {
	City string `json:"city"`
}

type weatherOutput struct {
	Temperature int `json:"temperature"`
}

func addWeatherTool(server *mcpsdk.Server, err error) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "weather", Description: "Get current weather"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		weatherInput,
	) (*mcpsdk.CallToolResult, weatherOutput, error) {
		return nil, weatherOutput{Temperature: 21}, err
	})
}

func TestInstrumentCapturesToolCall(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	Instrument(server, posthogmcp.New(queue),
		WithServerInfo("weather-server", "1.2.3"),
		WithIdentity(func(context.Context, *mcpsdk.CallToolRequest) (Identity, error) {
			return Identity{
				DistinctID:    "user-123",
				Groups:        posthog.Groups{"company": "acme"},
				SetProperties: posthog.Properties{"plan": "pro"},
			}, nil
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
	addWeatherTool(server, nil)

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	captures := queue.toolCalls(t)
	require.Len(t, captures, 1)
	capture := captures[0]
	assert.Equal(t, "user-123", capture.DistinctId)
	assert.Equal(t, posthog.Groups{"company": "acme"}, capture.Groups)
	properties := capture.Properties
	assert.Equal(t, "weather", properties["$mcp_tool_name"])
	assert.Equal(t, "weather-server", properties["$mcp_server_name"])
	assert.Equal(t, "1.2.3", properties["$mcp_server_version"])
	assert.Equal(t, "test-client", properties["$mcp_client_name"])
	assert.Equal(t, "2.0.0", properties["$mcp_client_version"])
	assert.Equal(t, "2025-11-25", properties["$mcp_protocol_version"])
	assert.Equal(t, false, properties["$mcp_is_error"])
	assert.Equal(t, posthog.Properties{"plan": "pro"}, properties["$set"])
	assert.Equal(t, "test", properties["environment"])
	assert.JSONEq(t, `{"city":"Melbourne"}`, jsonString(t, properties["$mcp_parameters"]))
	assert.JSONEq(t,
		`{"content":[{"type":"text","text":"{\"temperature\":21}"}],"structuredContent":{"temperature":21}}`,
		jsonString(t, properties["$mcp_response"]))
}

func TestInstrumentCapturesFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		addTool     func(*mcpsdk.Server)
		wantMessage string
	}{
		{
			name:        "typed handler error becomes an isError result",
			addTool:     func(server *mcpsdk.Server) { addWeatherTool(server, errors.New("forecast unavailable")) },
			wantMessage: "forecast unavailable",
		},
		{
			name: "raw handler error is a protocol error",
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
			server := newServer()
			Instrument(server, posthogmcp.New(queue))
			test.addTool(server)

			result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "weather",
				Arguments: map[string]any{"city": "Melbourne"},
			})
			if err == nil {
				require.True(t, result.IsError)
			}

			properties := queue.onlyToolCall(t)
			assert.Equal(t, true, properties["$mcp_is_error"])
			assert.Contains(t, properties["$mcp_error_message"], test.wantMessage)
			assert.Len(t, queue.exceptions(), 1)
		})
	}
}

func TestInstrumentPrivacyControlsAndUnrelatedMethods(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	Instrument(server, posthogmcp.New(queue), WithCaptureParameters(false), WithCaptureResponses(false))
	addWeatherTool(server, nil)

	client := connectInMemory(t, server)
	_, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	_, err = client.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	})
	require.NoError(t, err)

	properties := queue.onlyToolCall(t)
	assert.NotContains(t, properties, "$mcp_parameters")
	assert.NotContains(t, properties, "$mcp_response")
}

func TestInstrumentationFailuresDoNotChangeResponse(t *testing.T) {
	failingIdentity := WithIdentity(func(context.Context, *mcpsdk.CallToolRequest) (Identity, error) {
		return Identity{}, errors.New("identity unavailable")
	})
	panickingProperties := WithProperties(func(
		context.Context,
		*mcpsdk.CallToolRequest,
		*mcpsdk.CallToolResult,
		error,
	) (posthog.Properties, error) {
		panic("properties panic")
	})
	for _, test := range []struct {
		name         string
		queue        *fakeQueue
		opts         []Option
		wantReported []string
	}{
		{
			name:         "enqueue error",
			queue:        &fakeQueue{err: errors.New("queue full")},
			wantReported: []string{"posthogmcpsdk: capture: posthogmcp: enqueue $mcp_tool_call: queue full"},
		},
		{
			name:         "enqueue panic",
			queue:        &fakeQueue{panic: true},
			wantReported: []string{"posthogmcpsdk: capture: capture panic (string)"},
		},
		{
			name:  "resolver failures",
			queue: &fakeQueue{},
			opts:  []Option{failingIdentity, panickingProperties},
			wantReported: []string{
				"posthogmcpsdk: identity resolver: identity unavailable",
				"posthogmcpsdk: properties resolver: properties resolver panic (string)",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var reported []string
			server := newServer()
			opts := append(test.opts, WithErrorHandler(func(_ context.Context, err error) {
				mu.Lock()
				defer mu.Unlock()
				reported = append(reported, err.Error())
				panic("error handler panic")
			}))
			Instrument(server, posthogmcp.New(test.queue), opts...)
			addWeatherTool(server, nil)

			result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "weather",
				Arguments: map[string]any{"city": "Melbourne"},
			})
			require.NoError(t, err)
			assert.False(t, result.IsError)
			assert.JSONEq(t, `{"temperature":21}`, jsonString(t, result.StructuredContent))

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, test.wantReported, reported)
		})
	}
}

func TestMiddlewarePreservesDownstreamPanic(t *testing.T) {
	want := &struct{}{}
	handler := NewMiddleware(posthogmcp.New(&fakeQueue{}))(func(
		context.Context,
		string,
		mcpsdk.Request,
	) (mcpsdk.Result, error) {
		panic(want)
	})

	defer func() {
		assert.Same(t, want, recover())
	}()
	_, _ = handler(t.Context(), methodCallTool, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Name: "weather"}})
}

func TestMiddlewareMeasuresOnlyInnerMiddleware(t *testing.T) {
	var mu sync.Mutex
	var order []string
	appendOrder := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}

	queue := &fakeQueue{}
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
	Instrument(server, posthogmcp.New(queue), WithProperties(func(
		context.Context,
		*mcpsdk.CallToolRequest,
		*mcpsdk.CallToolResult,
		error,
	) (posthog.Properties, error) {
		appendOrder("capture")
		return nil, nil
	}))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ok"}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		appendOrder("handler")
		return nil, nil, nil
	})

	_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "ok"})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "inner-before,handler,inner-after,capture", strings.Join(order, ","))
}

func TestStreamableHTTPMapsSessionID(t *testing.T) {
	queue := &fakeQueue{}
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"},
		&mcpsdk.ServerOptions{GetSessionID: func() string { return "session-123" }},
	)
	Instrument(server, posthogmcp.New(queue))
	addWeatherTool(server, nil)

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne"},
	})
	require.NoError(t, err)
	assert.Equal(t, "session-123", queue.onlyToolCall(t)["$session_id"])
}

func newServer() *mcpsdk.Server {
	return mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
}

func connectInMemory(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "2.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func jsonString(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
