package posthogmcpgo

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcpsdk/v2"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolCallPropertiesMatchPosthogmcpsdk sends one call through each
// adapter and compares every property both events carry.
func TestToolCallPropertiesMatchPosthogmcpsdk(t *testing.T) {
	arguments := map[string]any{"city": "Melbourne", "units": 3, "context": "Checking the weather before a trip", "llm_model": "claude-opus-4-8"}
	for _, test := range []struct {
		name   string
		text   string
		result func(text string) (isError bool, err error)
	}{
		{name: "success", text: "Sunny, 22C", result: func(string) (bool, error) { return false, nil }},
		{name: "isError result", text: "city not found api_key=sk-live-123", result: func(string) (bool, error) { return true, nil }},
		{name: "returned error", result: func(string) (bool, error) { return false, errors.New("upstream timeout") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			isError, handlerErr := test.result(test.text)
			identity := func(context.Context) Identity {
				return Identity{DistinctID: "user-123", Groups: posthog.Groups{"company": "acme"}}
			}

			sdkQueue := &fakeQueue{}
			sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "weather-server", Version: "1.2.3"}, nil)
			posthogmcpsdk.Instrument(sdkServer, posthogmcp.New(sdkQueue),
				posthogmcpsdk.WithServerInfo("weather-server", "1.2.3"),
				posthogmcpsdk.WithIdentity(func(ctx context.Context, _ *mcpsdk.CallToolRequest) (posthogmcpsdk.Identity, error) {
					return posthogmcpsdk.Identity(identity(ctx)), nil
				}))
			sdkServer.AddTool(&mcpsdk.Tool{
				Name:        "weather",
				Description: "Get current weather",
				Meta:        mcpsdk.Meta{"category": "forecasts"},
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}, "units": map[string]any{"type": "number"}}, "required": []any{"city"}},
			}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				if handlerErr != nil {
					return nil, handlerErr
				}
				return &mcpsdk.CallToolResult{IsError: isError, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: test.text}}}, nil
			})
			sdkClient := connectGoSDK(t, sdkServer)
			_, err := sdkClient.ListTools(t.Context(), nil)
			require.NoError(t, err)
			_, _ = sdkClient.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "weather", Arguments: arguments})

			queue := &fakeQueue{}
			handler := &weatherHandler{err: handlerErr, result: &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{mcp.NewTextContent(test.text)}}}
			c := connectInProcess(t, newInstrumentedServer(queue, weatherTool(mcp.WithNumber("units")), handler,
				WithIdentity(func(ctx context.Context, _ mcp.CallToolRequest) (Identity, error) { return identity(ctx), nil })))
			_, _ = callTool(t, c, "weather", arguments)

			want, got := sdkQueue.onlyToolCall(t), queue.onlyToolCall(t)
			require.NotContains(t, want, "$mcp_conversation_id", "a one-client connection mints no conversation handle")
			for _, volatile := range []string{"$mcp_duration_ms", "$session_id"} {
				assert.Contains(t, got, volatile)
				delete(want, volatile)
				delete(got, volatile)
			}
			require.NotEmpty(t, want["$mcp_protocol_version"])
			assert.JSONEq(t, jsonString(t, want), jsonString(t, got))

			sdkExceptions, exceptions := sdkQueue.exceptions(), queue.exceptions()
			require.Len(t, exceptions, len(sdkExceptions))
			for i := range exceptions {
				assert.Equal(t, sdkExceptions[i].ExceptionList, exceptions[i].ExceptionList)
			}
		})
	}
}

func connectGoSDK(t *testing.T, s *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := s.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "2.0.0"}, nil).Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return session
}
