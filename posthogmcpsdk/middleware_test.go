package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
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

type weatherInput struct {
	City string `json:"city"`
}

type weatherOutput struct {
	Temperature int `json:"temperature"`
}

func addWeatherTool(server *mcpsdk.Server, err error) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "weather",
		Description: "Get current weather",
		Meta:        mcpsdk.Meta{"category": "forecasts"},
	}, func(
		context.Context,
		*mcpsdk.CallToolRequest,
		weatherInput,
	) (*mcpsdk.CallToolResult, weatherOutput, error) {
		return nil, weatherOutput{Temperature: 21}, err
	})
}

func TestInstrumentCapturesToolCallEndToEnd(t *testing.T) {
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

	client := connectInMemory(t, server)
	_, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	result, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne", "context": "Checking the weather before a trip"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "the typed tool rejected the injected context argument")

	captures := queue.toolCalls()
	require.Len(t, captures, 1)
	capture := captures[0]
	assert.Equal(t, "user-123", capture.DistinctId)
	assert.Equal(t, posthog.Groups{"company": "acme"}, capture.Groups)
	properties := capture.Properties
	assert.IsType(t, float64(0), properties["$mcp_duration_ms"])
	delete(properties, "$mcp_duration_ms")
	assert.Regexp(t, generatedSessionID, properties["$session_id"])
	delete(properties, "$session_id")
	// Protocol version and result shape follow the go-sdk version under test.
	assert.Equal(t, client.InitializeResult().ProtocolVersion, properties["$mcp_protocol_version"])
	delete(properties, "$mcp_protocol_version")
	received := *result
	received.Meta = nil // go-sdk v1.8+ stamps serverInfo on the way out, after the middleware
	assert.JSONEq(t, jsonString(t, &received), jsonString(t, properties["$mcp_response"]))
	delete(properties, "$mcp_response")
	assert.JSONEq(t, `{
		"$groups": {"company": "acme"},
		"$mcp_client_name": "test-client",
		"$mcp_client_version": "2.0.0",
		"$mcp_intent": "Checking the weather before a trip",
		"$mcp_intent_source": "context_parameter",
		"$mcp_is_error": false,
		"$mcp_parameters": {"request": {"method": "tools/call", "params": {"name": "weather", "arguments": {"city": "Melbourne"}}}},
		"$mcp_resource_name": "weather",
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

func TestInstrumentAdvertisesAnalyticsArguments(t *testing.T) {
	for _, test := range []struct {
		name           string
		opts           []Option
		inputSchema    any
		wantProperties []string
		wantRequired   []string
	}{
		{
			name:           "added to a closed schema",
			inputSchema:    map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []any{"city"}, "additionalProperties": false},
			wantProperties: []string{"city", "context", "llm_model", "conversation_id"},
			wantRequired:   []string{"city", "context", "llm_model"},
		},
		{
			name:           "added to an empty schema",
			inputSchema:    map[string]any{"type": "object"},
			wantProperties: []string{"context", "llm_model", "conversation_id"},
			wantRequired:   []string{"context", "llm_model"},
		},
		{
			name:           "a tool's own arguments are kept",
			inputSchema:    map[string]any{"type": "object", "properties": map[string]any{"context": map[string]any{"type": "object"}, "llm_model": map[string]any{"type": "object"}, "conversation_id": map[string]any{"type": "object"}}},
			wantProperties: []string{"context", "llm_model", "conversation_id"},
		},
		{
			name:        "combinator schemas are left alone",
			inputSchema: map[string]any{"type": "object", "anyOf": []any{map[string]any{"required": []any{"id"}}}},
		},
		{
			name:           "context disabled",
			opts:           []Option{WithContextParameter(false)},
			inputSchema:    map[string]any{"type": "object"},
			wantProperties: []string{"llm_model", "conversation_id"},
			wantRequired:   []string{"llm_model"},
		},
		{
			name:           "model capture disabled",
			opts:           []Option{WithCaptureModel(false)},
			inputSchema:    map[string]any{"type": "object"},
			wantProperties: []string{"context", "conversation_id"},
			wantRequired:   []string{"context"},
		},
		{
			name:           "conversation anchoring disabled",
			opts:           []Option{WithConversationID(false)},
			inputSchema:    map[string]any{"type": "object"},
			wantProperties: []string{"context", "llm_model"},
			wantRequired:   []string{"context", "llm_model"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newServer()
			Instrument(server, posthogmcp.New(&fakeQueue{}), test.opts...)
			server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: test.inputSchema}, echoHandler)
			client := connectInMemory(t, server)

			for range 2 {
				result, err := client.ListTools(t.Context(), nil)
				require.NoError(t, err)
				require.Len(t, result.Tools, 1)
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				}
				require.NoError(t, json.Unmarshal([]byte(jsonString(t, result.Tools[0].InputSchema)), &schema))
				assert.ElementsMatch(t, test.wantProperties, slices.Collect(maps.Keys(schema.Properties)))
				assert.Equal(t, test.wantRequired, schema.Required)
			}
		})
	}
}

// argumentsEchoTool registers a tool without input validation that returns
// the arguments it received as text.
func argumentsEchoTool(server *mcpsdk.Server, inputSchema any) {
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: inputSchema}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(req.Params.Arguments)}}}, nil
	})
}

func TestInstrumentCapturesModel(t *testing.T) {
	ownModel := map[string]any{"type": "object", "properties": map[string]any{"llm_model": map[string]any{"type": "string"}}}
	for _, test := range []struct {
		name          string
		opts          []Option
		inputSchema   any
		meta          mcpsdk.Meta
		arguments     map[string]any
		wantModel     any
		wantSource    any
		wantArguments string
	}{
		{
			name:          "self-reported through the injected argument",
			arguments:     map[string]any{"q": "flags", "llm_model": " claude-opus-4-8 "},
			wantModel:     "claude-opus-4-8",
			wantSource:    "self_reported",
			wantArguments: `{"q":"flags"}`,
		},
		{
			name:          "codex turn metadata wins over the argument",
			meta:          mcpsdk.Meta{"x-codex-turn-metadata": map[string]any{"model": "gpt-5.2"}},
			arguments:     map[string]any{"llm_model": "claude-opus-4-8"},
			wantModel:     "gpt-5.2",
			wantSource:    "client_metadata",
			wantArguments: `{}`,
		},
		{
			name:          "the proposed aiInvocation metadata",
			meta:          mcpsdk.Meta{"io.modelcontextprotocol/aiInvocation": map[string]any{"model": "gemini-3-pro"}},
			arguments:     map[string]any{},
			wantModel:     "gemini-3-pro",
			wantSource:    "client_metadata",
			wantArguments: `{}`,
		},
		{
			name:          "unknown metadata falls back to the argument",
			meta:          mcpsdk.Meta{"x-codex-turn-metadata": map[string]any{"model": "Unknown"}},
			arguments:     map[string]any{"llm_model": "claude-opus-4-8"},
			wantModel:     "claude-opus-4-8",
			wantSource:    "self_reported",
			wantArguments: `{}`,
		},
		{
			name:          "an unknown self-report is not a model",
			arguments:     map[string]any{"llm_model": "unknown"},
			wantArguments: `{}`,
		},
		{
			name:          "a tool's own llm_model is its data",
			inputSchema:   ownModel,
			arguments:     map[string]any{"llm_model": "claude-opus-4-8"},
			wantArguments: `{"llm_model":"claude-opus-4-8"}`,
		},
		{
			name:          "disabled",
			opts:          []Option{WithCaptureModel(false)},
			meta:          mcpsdk.Meta{"x-codex-turn-metadata": map[string]any{"model": "gpt-5.2"}},
			arguments:     map[string]any{"llm_model": "claude-opus-4-8"},
			wantArguments: `{"llm_model":"claude-opus-4-8"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue), test.opts...)
			inputSchema := test.inputSchema
			if inputSchema == nil {
				inputSchema = map[string]any{"type": "object"}
			}
			argumentsEchoTool(server, inputSchema)

			result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Meta:      test.meta,
				Name:      "echo",
				Arguments: test.arguments,
			})
			require.NoError(t, err)
			assert.JSONEq(t, test.wantArguments, result.Content[0].(*mcpsdk.TextContent).Text)

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantModel, properties["$mcp_llm_model"])
			assert.Equal(t, test.wantSource, properties["$mcp_llm_model_source"])
			assert.JSONEq(t, test.wantArguments, jsonString(t, properties["$mcp_parameters"].(map[string]any)["request"].(map[string]any)["params"].(map[string]any)["arguments"]))
		})
	}
}

func TestInstrumentDispatchesContextArgument(t *testing.T) {
	type ownContextInput struct {
		Context string `json:"context"`
	}
	for _, test := range []struct {
		name        string
		opts        []Option
		listFirst   bool
		ownsContext bool
		wantResult  string
		wantIntent  any
	}{
		{
			name:       "removed after a listing",
			listFirst:  true,
			wantResult: "",
			wantIntent: "Planning a trip",
		},
		{
			name:       "removed on a replica that never served the listing",
			wantResult: "",
			wantIntent: "Planning a trip",
		},
		{
			name:        "kept for a tool that declares it",
			listFirst:   true,
			ownsContext: true,
			wantResult:  "Planning a trip",
			wantIntent:  "Planning a trip",
		},
		{
			name:        "kept and not captured as intent when disabled",
			opts:        []Option{WithContextParameter(false)},
			listFirst:   true,
			ownsContext: true,
			wantResult:  "Planning a trip",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue), test.opts...)
			tool := &mcpsdk.Tool{Name: "plan", Description: "Plan a trip"}
			if test.ownsContext {
				mcpsdk.AddTool(server, tool, func(_ context.Context, _ *mcpsdk.CallToolRequest, in ownContextInput) (*mcpsdk.CallToolResult, any, error) {
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: in.Context}}}, nil, nil
				})
			} else {
				mcpsdk.AddTool(server, tool, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{}}}, nil, nil
				})
			}
			client := connectInMemory(t, server)
			if test.listFirst {
				_, err := client.ListTools(t.Context(), nil)
				require.NoError(t, err)
			}

			result, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "plan",
				Arguments: map[string]any{"context": "Planning a trip"},
			})
			require.NoError(t, err)
			require.False(t, result.IsError, toolResultText(result))
			assert.Equal(t, test.wantResult, result.Content[0].(*mcpsdk.TextContent).Text)

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantIntent, properties["$mcp_intent"])
			assert.Equal(t, "Plan a trip", properties["$mcp_tool_description"])
		})
	}
}

func echoHandler(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return &mcpsdk.CallToolResult{}, nil
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
			assert.Equal(t, test.wantMessage, properties["$mcp_error_message"])
			assert.Len(t, queue.exceptions(), 1)
		})
	}
}

func TestInstrumentCapturesParametersAndIntent(t *testing.T) {
	for _, test := range []struct {
		name           string
		arguments      any
		wantParameters string
		wantIntent     any
		wantSource     any
	}{
		{
			name:           "context is captured as intent, not parameters, and the injected arguments are left out",
			arguments:      map[string]any{"city": "Melbourne", "context": "  Checking the weather for a trip  ", "conversation_id": "c-1", "llm_model": "unknown"},
			wantParameters: `{"request":{"method":"tools/call","params":{"name":"echo","arguments":{"city":"Melbourne"}}}}`,
			wantIntent:     "Checking the weather for a trip",
			wantSource:     "context_parameter",
		},
		{
			name:           "absent arguments are an empty object",
			wantParameters: `{"request":{"method":"tools/call","params":{"name":"echo","arguments":{}}}}`,
		},
		{
			name:           "a non-string context is not an intent",
			arguments:      map[string]any{"context": 42, "limit": 1.50},
			wantParameters: `{"request":{"method":"tools/call","params":{"name":"echo","arguments":{"limit":1.5}}}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue))
			server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, echoHandler)

			_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "echo",
				Arguments: test.arguments,
			})
			require.NoError(t, err)

			properties := queue.onlyToolCall(t)
			assert.JSONEq(t, test.wantParameters, jsonString(t, properties["$mcp_parameters"]))
			assert.Equal(t, test.wantIntent, properties["$mcp_intent"])
			assert.Equal(t, test.wantSource, properties["$mcp_intent_source"])
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

func TestInstrumentIntentFallback(t *testing.T) {
	inferFromCall := func(_ context.Context, req *mcpsdk.CallToolRequest) (string, error) {
		return "Looking up " + req.Params.Name + " " + string(req.Params.Arguments), nil
	}
	fixed := func(intent string, err error) IntentFallback {
		return func(context.Context, *mcpsdk.CallToolRequest) (string, error) { return intent, err }
	}
	for _, test := range []struct {
		name         string
		opts         []Option
		fallback     IntentFallback
		arguments    map[string]any
		wantCalls    int
		wantIntent   any
		wantSource   any
		wantReported []string
	}{
		{
			name:       "inferred when the agent sends no context, from the request as the client sent it",
			fallback:   inferFromCall,
			arguments:  map[string]any{"city": "Melbourne", "llm_model": "m-1"},
			wantCalls:  1,
			wantIntent: `Looking up weather {"city":"Melbourne","llm_model":"m-1"}`,
			wantSource: "inferred",
		},
		{
			name:       "inferred when the context is blank",
			fallback:   fixed("Checking the forecast", nil),
			arguments:  map[string]any{"city": "Melbourne", "context": "  "},
			wantCalls:  1,
			wantIntent: "Checking the forecast",
			wantSource: "inferred",
		},
		{
			name:       "inferred when the context is an empty object",
			fallback:   fixed("Checking the forecast", nil),
			arguments:  map[string]any{"city": "Melbourne", "context": "{}"},
			wantCalls:  1,
			wantIntent: "Checking the forecast",
			wantSource: "inferred",
		},
		{
			name:       "inferred when the context is not a string",
			fallback:   fixed("Checking the forecast", nil),
			arguments:  map[string]any{"city": "Melbourne", "context": 42},
			wantCalls:  1,
			wantIntent: "Checking the forecast",
			wantSource: "inferred",
		},
		{
			name:       "the agent's context wins and the fallback is not asked",
			fallback:   fixed("Checking the forecast", nil),
			arguments:  map[string]any{"city": "Melbourne", "context": "Planning a trip"},
			wantIntent: "Planning a trip",
			wantSource: "context_parameter",
		},
		{
			name:       "inferred when the context parameter is off",
			opts:       []Option{WithContextParameter(false)},
			fallback:   fixed("Checking the forecast", nil),
			arguments:  map[string]any{"city": "Melbourne"},
			wantCalls:  1,
			wantIntent: "Checking the forecast",
			wantSource: "inferred",
		},
		{
			name:       "personal data is redacted like any intent",
			fallback:   fixed("Emailing alice@example.com", nil),
			arguments:  map[string]any{"city": "Melbourne"},
			wantCalls:  1,
			wantIntent: "Emailing [redacted]",
			wantSource: "inferred",
		},
		{
			name:      "a blank result is no intent",
			fallback:  fixed("  ", nil),
			arguments: map[string]any{"city": "Melbourne"},
			wantCalls: 1,
		},
		{
			name:      "an empty object result is no intent",
			fallback:  fixed("{}", nil),
			arguments: map[string]any{"city": "Melbourne"},
			wantCalls: 1,
		},
		{
			name:         "an error is reported and leaves no intent",
			fallback:     fixed("ignored", errors.New("model unavailable")),
			arguments:    map[string]any{"city": "Melbourne"},
			wantCalls:    1,
			wantReported: []string{"posthogmcpsdk: intent fallback: model unavailable"},
		},
		{
			name: "a panic is reported and leaves no intent",
			fallback: func(context.Context, *mcpsdk.CallToolRequest) (string, error) {
				panic("fallback panic")
			},
			arguments:    map[string]any{"city": "Melbourne"},
			wantCalls:    1,
			wantReported: []string{"posthogmcpsdk: intent fallback: intent fallback panic (string)"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reported []string
			var calls int
			queue := &fakeQueue{}
			server := newServer()
			opts := append(test.opts,
				WithIntentFallback(func(ctx context.Context, req *mcpsdk.CallToolRequest) (string, error) {
					calls++
					return test.fallback(ctx, req)
				}),
				WithErrorHandler(func(_ context.Context, err error) { reported = append(reported, err.Error()) }),
			)
			Instrument(server, posthogmcp.New(queue), opts...)
			addWeatherTool(server, nil)

			result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "weather",
				Arguments: test.arguments,
			})
			require.NoError(t, err)
			require.False(t, result.IsError, toolResultText(result))

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantCalls, calls)
			assert.Equal(t, test.wantIntent, properties["$mcp_intent"])
			assert.Equal(t, test.wantSource, properties["$mcp_intent_source"])
			assert.Equal(t, test.wantReported, reported)
		})
	}
}

func TestInstrumentIntentFallbackIsForToolCallsOnly(t *testing.T) {
	for _, test := range []struct {
		name      string
		tool      string
		wantEvent string
	}{
		{name: "an unknown tool", tool: "no_such_tool", wantEvent: "$mcp_unknown_tool"},
		{name: "the missing-capability tool", tool: "get_more_tools", wantEvent: "$mcp_missing_capability"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue),
				WithMissingCapabilityTool(""),
				WithIntentFallback(func(context.Context, *mcpsdk.CallToolRequest) (string, error) {
					calls++
					return "inferred", nil
				}),
			)
			addWeatherTool(server, nil)

			_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      test.tool,
				Arguments: map[string]any{"context": "Reporting a gap"},
			})
			if test.tool == "no_such_tool" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Zero(t, calls)
			assert.Empty(t, queue.toolCalls())
			require.Len(t, queue.captures(test.wantEvent), 1)
			assert.NotEqual(t, "inferred", queue.captures(test.wantEvent)[0].Properties["$mcp_intent_source"])
		})
	}
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
	panickingInnerListing := func(server *mcpsdk.Server) {
		server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
			return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
				if method == methodListTools {
					panic("inner tools/list panic")
				}
				return next(ctx, method, req)
			}
		})
	}
	for _, test := range []struct {
		name         string
		queue        *fakeQueue
		opts         []Option
		inner        func(*mcpsdk.Server)
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
		{
			name:         "inner handler panic while learning tools",
			queue:        &fakeQueue{},
			inner:        panickingInnerListing,
			wantReported: []string{"posthogmcpsdk: tools/list through inner handler panicked (string)"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var reported []string
			server := newServer()
			if test.inner != nil {
				test.inner(server)
			}
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
	handler := NewMiddleware(posthogmcp.New(&fakeQueue{})).Receiving(func(
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
	assert.Equal(t, "ses_346bdc9a6b5cb06913bb476a65021eb5", queue.onlyToolCall(t)["$session_id"])
}

type headerTransport struct{ header http.Header }

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for name, values := range h.header {
		req.Header[name] = values
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestStreamableHTTPCapturesClientHeaders(t *testing.T) {
	for _, test := range []struct {
		name          string
		header        http.Header
		wantUserAgent any
		wantVendor    any
	}{
		{
			name:          "both headers",
			header:        http.Header{"User-Agent": {"claude-code/2.1.0 (claude-vscode)"}, "X-Anthropic-Client": {"claude-vscode"}},
			wantUserAgent: "claude-code/2.1.0 (claude-vscode)",
			wantVendor:    "claude-vscode",
		},
		{
			name:          "no vendor header",
			header:        http.Header{"User-Agent": {"cursor/1.0"}},
			wantUserAgent: "cursor/1.0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue))
			addWeatherTool(server, nil)

			handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
				Endpoint:   httpServer.URL,
				HTTPClient: &http.Client{Transport: headerTransport{test.header}},
			}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			_, err = session.CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "weather",
				Arguments: map[string]any{"city": "Melbourne"},
			})
			require.NoError(t, err)
			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantUserAgent, properties["$mcp_client_user_agent"])
			assert.Equal(t, test.wantVendor, properties["$mcp_vendor_client"])
		})
	}
}

// Without a conversation handle, a stateless HTTP server shares a session
// across requests only through an Mcp-Session-Id the client echoes; without
// one, each request is its own session. Which one a client holds depends on
// the negotiated revision.
func TestStatelessHTTPSessions(t *testing.T) {
	for _, test := range []struct {
		name         string
		getSessionID func() string
		client       http.RoundTripper
	}{
		{name: "the client echoes the server's session id"},
		{name: "the client drops the server's session id", client: sessionIDDroppingTransport{}},
		{name: "no session id", getSessionID: func() string { return "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := mcpsdk.NewServer(
				&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"},
				&mcpsdk.ServerOptions{GetSessionID: test.getSessionID},
			)
			Instrument(server, posthogmcp.New(queue), WithConversationID(false))
			addWeatherTool(server, nil)

			handler := mcpsdk.NewStreamableHTTPHandler(
				func(*http.Request) *mcpsdk.Server { return server },
				&mcpsdk.StreamableHTTPOptions{Stateless: true},
			)
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
				Endpoint:   httpServer.URL,
				HTTPClient: &http.Client{Transport: test.client},
			}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			for range 2 {
				result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
					Name:      "weather",
					Arguments: map[string]any{"city": "Melbourne", "context": "Checking the weather"},
				})
				require.NoError(t, err)
				require.False(t, result.IsError, toolResultText(result))
			}

			captures := queue.toolCalls()
			require.Len(t, captures, 2)
			for _, capture := range captures {
				assert.Equal(t, "Checking the weather", capture.Properties["$mcp_intent"])
				assert.Equal(t, session.InitializeResult().ProtocolVersion, capture.Properties["$mcp_protocol_version"])
			}
			first, second := captures[0].Properties["$session_id"], captures[1].Properties["$session_id"]
			if session.ID() != "" && test.client == nil {
				assert.Equal(t, deterministicSessionID(session.ID()), first)
				assert.Equal(t, first, second)
			} else {
				assert.Regexp(t, generatedSessionID, first)
				assert.NotEqual(t, first, second)
			}
		})
	}
}

func newServer() *mcpsdk.Server {
	return mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
}

func connectInMemory(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	return connectInMemoryWith(t, server, nil)
}

func connectInMemoryWith(t *testing.T, server *mcpsdk.Server, opts *mcpsdk.ClientOptions) *mcpsdk.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "2.0.0"}, opts)
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

func TestInstrumentLearnsToolsOncePerCatalog(t *testing.T) {
	for _, test := range []struct {
		name             string
		toolCount        int
		calls            []string
		wantPages        int
		wantDescriptions []any
		wantUnknown      int
	}{
		{
			name:             "a tool on the last page",
			toolCount:        5,
			calls:            []string{"tool-005"},
			wantPages:        5,
			wantDescriptions: []any{"Tool 5"},
		},
		{
			name:             "tools on the first and last pages",
			toolCount:        5,
			calls:            []string{"tool-001", "tool-005"},
			wantPages:        5,
			wantDescriptions: []any{"Tool 1", "Tool 5"},
		},
		{
			name:        "repeated unknown names",
			toolCount:   5,
			calls:       []string{"nope", "nope", "other"},
			wantPages:   5,
			wantUnknown: 3,
		},
		{
			name:             "a tool beyond the page cap",
			toolCount:        101,
			calls:            []string{"tool-101"},
			wantPages:        100,
			wantDescriptions: []any{nil},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server, pages := newPagedServer(test.toolCount)
			Instrument(server, posthogmcp.New(queue))
			client := connectInMemory(t, server)

			for _, name := range test.calls {
				_, _ = client.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{}})
			}

			assert.Equal(t, test.wantPages, int(pages.Load()))
			var descriptions []any
			for _, capture := range queue.toolCalls() {
				descriptions = append(descriptions, capture.Properties["$mcp_tool_description"])
			}
			assert.Equal(t, test.wantDescriptions, descriptions)
			assert.Len(t, queue.captures("$mcp_unknown_tool"), test.wantUnknown)
		})
	}
}

func TestInstrumentRecoversFromATransientListingFailure(t *testing.T) {
	queue := &fakeQueue{}
	server, pages := newPagedServer(3)
	var failOnce atomic.Bool
	failOnce.Store(true)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodListTools && failOnce.Swap(false) {
				return nil, errors.New("transient")
			}
			return next(ctx, method, req)
		}
	})
	var reported []string
	Instrument(server, posthogmcp.New(queue), WithErrorHandler(func(_ context.Context, err error) {
		reported = append(reported, err.Error())
	}))
	client := connectInMemory(t, server)

	for range 3 {
		_, _ = client.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "tool-003", Arguments: map[string]any{}})
	}

	assert.Equal(t, 3, int(pages.Load()))
	var descriptions []any
	for _, capture := range queue.toolCalls() {
		descriptions = append(descriptions, capture.Properties["$mcp_tool_description"])
	}
	assert.Equal(t, []any{nil, "Tool 3", "Tool 3"}, descriptions)
	assert.Equal(t, []string{"posthogmcpsdk: tools/list through inner handler: transient"}, reported)
}

func TestInstrumentConcurrentUnknownCallsShareOneWalk(t *testing.T) {
	const calls = 4
	var entered sync.WaitGroup
	entered.Add(calls)
	allEntered := make(chan struct{})
	go func() {
		entered.Wait()
		close(allEntered)
	}()

	server, pages := newPagedServer(5)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodListTools {
				select {
				case <-allEntered:
				case <-time.After(5 * time.Second):
				}
			}
			return next(ctx, method, req)
		}
	})
	Instrument(server, posthogmcp.New(&fakeQueue{}))
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodCallTool {
				entered.Done()
			}
			return next(ctx, method, req)
		}
	})
	client := connectInMemory(t, server)

	var done sync.WaitGroup
	for range calls {
		done.Go(func() {
			_, _ = client.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "nope"})
		})
	}
	done.Wait()

	assert.Equal(t, 5, int(pages.Load()))
}

// newPagedServer returns a server listing one tool per page, named in listing
// order from tool-001, and a count of the tools/list pages its handler serves.
func newPagedServer(n int) (*mcpsdk.Server, *atomic.Int32) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, &mcpsdk.ServerOptions{PageSize: 1})
	for i := 1; i <= n; i++ {
		server.AddTool(&mcpsdk.Tool{
			Name:        fmt.Sprintf("tool-%03d", i),
			Description: fmt.Sprintf("Tool %d", i),
			InputSchema: map[string]any{"type": "object"},
		}, echoHandler)
	}
	pages := &atomic.Int32{}
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == methodListTools {
				pages.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	return server, pages
}

func TestInstrumentRelearnsToolsAfterListChanged(t *testing.T) {
	type ownContextInput struct {
		Context string `json:"context"`
	}
	withoutContext := func(server *mcpsdk.Server) {
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "plan"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "no context"}}}, nil, nil
		})
	}
	withOwnContext := func(server *mcpsdk.Server) {
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "plan"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in ownContextInput) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "own context: " + in.Context}}}, nil, nil
		})
	}
	for _, test := range []struct {
		name       string
		before     func(*mcpsdk.Server)
		after      func(*mcpsdk.Server)
		wantResult string
	}{
		{
			name:       "the new schema declares context",
			before:     withoutContext,
			after:      withOwnContext,
			wantResult: "own context: Planning a trip",
		},
		{
			name:       "the old schema declared context and the new one does not",
			before:     withOwnContext,
			after:      withoutContext,
			wantResult: "no context",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newServer()
			Instrument(server, posthogmcp.New(&fakeQueue{}))
			test.before(server)
			listChanged := make(chan struct{}, 1)
			client := connectInMemoryWith(t, server, &mcpsdk.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
					select {
					case listChanged <- struct{}{}:
					default:
					}
				},
			})
			_, err := client.ListTools(t.Context(), nil)
			require.NoError(t, err)

			server.RemoveTools("plan")
			test.after(server)
			<-listChanged

			result, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name:      "plan",
				Arguments: map[string]any{"context": "Planning a trip"},
			})
			require.NoError(t, err)
			require.False(t, result.IsError, toolResultText(result))
			assert.Equal(t, test.wantResult, result.Content[0].(*mcpsdk.TextContent).Text)
		})
	}
}

func TestInstrumentRelearnsAToolReplacedWhileNoSessionIsConnected(t *testing.T) {
	type ownContextInput struct {
		Context string `json:"context"`
	}
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	server := newServer()
	Instrument(server, posthogmcp.New(&fakeQueue{}), withClock(clock))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "plan"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})

	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	listing, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "listing-client", Version: "1.0.0"}, nil).Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	_, err = listing.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, listing.Close())
	require.NoError(t, serverSession.Wait())

	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "plan"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in ownContextInput) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "own context: " + in.Context}}}, nil, nil
	})
	clock.advance(10 * time.Second)

	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "plan",
		Arguments: map[string]any{"context": "Planning a trip"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, toolResultText(result))
	assert.Equal(t, "own context: Planning a trip", result.Content[0].(*mcpsdk.TextContent).Text)
}

func TestNilAnalyticsPanicsAtConstruction(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func()
	}{
		{name: "NewMiddleware", install: func() { NewMiddleware(nil) }},
		{name: "Instrument", install: func() { Instrument(newServer(), nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.PanicsWithValue(t, "posthogmcpsdk: analytics must not be nil", test.install)
		})
	}
}

func TestToolResultText(t *testing.T) {
	var nilText *mcpsdk.TextContent
	for _, test := range []struct {
		name    string
		content []mcpsdk.Content
		want    string
	}{
		{"joins text blocks", []mcpsdk.Content{&mcpsdk.TextContent{Text: "upstream"}, &mcpsdk.ImageContent{}, &mcpsdk.TextContent{Text: "timed out"}}, "upstream timed out"},
		{"skips a typed nil text block", []mcpsdk.Content{nilText, &mcpsdk.TextContent{Text: "denied"}}, "denied"},
		{"no text", []mcpsdk.Content{&mcpsdk.ImageContent{}}, "Unknown error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, toolResultText(&mcpsdk.CallToolResult{Content: test.content}))
		})
	}
}

type upstreamTimeout struct{}

func (upstreamTimeout) Error() string { return "upstream timed out" }

func TestInstrumentRecoversATypedHandlersError(t *testing.T) {
	for _, test := range []struct {
		name          string
		handler       func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error)
		wantErrorType string
		wantMessage   string
	}{
		{
			name: "typed handler returns its own error type",
			handler: func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
				return nil, nil, upstreamTimeout{}
			},
			wantErrorType: "posthogmcpsdk.upstreamTimeout",
			wantMessage:   "upstream timed out",
		},
		{
			name: "typed handler returns a plain error",
			handler: func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
				return nil, nil, errors.New("boom")
			},
			wantErrorType: "Error",
			wantMessage:   "boom",
		},
		{
			name: "handler returns an error result of its own",
			handler: func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
				return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "403 Forbidden"}}}, nil, nil
			},
			wantErrorType: "Error",
			wantMessage:   "403 Forbidden",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newServer()
			Instrument(server, posthogmcp.New(queue))
			mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "query"}, test.handler)

			_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "query"})
			require.NoError(t, err)

			call := queue.onlyToolCall(t)
			assert.Equal(t, test.wantErrorType, call["$mcp_error_type"])
			assert.Equal(t, test.wantMessage, call["$mcp_error_message"])
		})
	}
}
