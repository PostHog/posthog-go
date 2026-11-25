package posthogmcpgo

import (
	"context"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	chatGPTUserAgent = "openai-mcp/1.0.0 (ChatGPT)"
	chatGPTSession   = "ses_2f5d35b7108d17c807766f2f769a1e8c"
	codexSession     = "ses_0fdf126b8e2c1f9689ca12f3dd3383e6"
	vsCodeSession    = "ses_0571acb894e6753d33e37e5c2b327c59"
)

func connectHTTPAs(t *testing.T, s *server.MCPServer, userAgent, clientName string, opts ...server.StreamableHTTPOption) *client.Client {
	t.Helper()
	httpServer := server.NewTestStreamableHTTPServer(s, opts...)
	t.Cleanup(httpServer.Close)
	c, err := client.NewStreamableHttpClient(httpServer.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{"User-Agent": userAgent}))
	require.NoError(t, err)
	require.NoError(t, c.Start(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: "1.0.0"}
	_, err = c.Initialize(t.Context(), init)
	require.NoError(t, err)
	return c
}

func callWithMeta(t *testing.T, c *client.Client, meta map[string]any) error {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "weather"
	req.Params.Arguments = map[string]any{"city": "Melbourne"}
	if meta != nil {
		req.Params.Meta = &mcp.Meta{AdditionalFields: meta}
	}
	_, err := c.CallTool(t.Context(), req)
	return err
}

func TestClientConversationIDs(t *testing.T) {
	const generated = "generated"
	stateless := server.WithStateLess(true)
	for _, test := range []struct {
		name        string
		userAgent   string
		clientName  string
		stateful    bool
		meta        map[string]any
		wantSession string
		wantSource  string
	}{
		{name: "a chatgpt id sets the session", userAgent: chatGPTUserAgent, meta: map[string]any{"openai/session": "v1/conv-example-0001"}, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "a chatgpt id wins over a transport session", userAgent: chatGPTUserAgent, stateful: true, meta: map[string]any{"openai/session": "v1/conv-example-0001"}, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "an invalid chatgpt id is a fresh session", userAgent: chatGPTUserAgent, meta: map[string]any{"openai/session": ""}, wantSession: generated, wantSource: "generated"},
		{name: "a codex id sets the session", userAgent: "codex-mcp-client/0.50.0", meta: map[string]any{"sessionId": "019A4F2E-1C3D-7B8A-9E0F-1A2B3C4D5E6F"}, wantSession: codexSession, wantSource: "client_metadata"},
		{name: "a vs code id sets the session by client name", clientName: "Visual Studio Code", stateful: true, meta: map[string]any{"vscode.conversationId": "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e"}, wantSession: vsCodeSession, wantSource: "client_metadata"},
		{name: "a key is read only from the client that defines it", userAgent: "example-client/1.0", meta: map[string]any{"sessionId": "019a4f2e-1c3d-7b8a-9e0f-1a2b3c4d5e6f", "openai/session": "v1/conv-example-0001"}, wantSession: generated, wantSource: "generated"},
		{name: "a transport session", userAgent: "example-client/1.0", stateful: true, wantSession: "transport", wantSource: "transport"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			var opts []server.StreamableHTTPOption
			if !test.stateful {
				opts = append(opts, stateless)
			}
			clientName := test.clientName
			if clientName == "" {
				clientName = "test-client"
			}
			c := connectHTTPAs(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{}), test.userAgent, clientName, opts...)
			require.NoError(t, callWithMeta(t, c, test.meta))

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantSource, properties["$mcp_session_source"])
			switch test.wantSession {
			case generated:
				assert.Regexp(t, generatedSessionID, properties["$session_id"])
			case "transport":
				assert.Equal(t, deterministicSessionID(c.GetSessionId()), properties["$session_id"])
			default:
				assert.Equal(t, test.wantSession, properties["$session_id"])
			}
		})
	}
}

func TestOneClientConnectionsAreGenerated(t *testing.T) {
	queue := &fakeQueue{}
	c := connectInProcess(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{}))
	require.NoError(t, callWithMeta(t, c, nil))
	assert.Equal(t, "generated", queue.onlyToolCall(t)["$mcp_session_source"])
}

func TestCustomEventsKeepTheirCallsSession(t *testing.T) {
	const otherUserAgent = "example-client/1.0"
	for _, test := range []struct {
		name               string
		middlewareCaptures bool
	}{
		{name: "hooks capture"},
		{name: "middleware captures", middlewareCaptures: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			release := map[string]chan struct{}{chatGPTUserAgent: make(chan struct{}), otherUserAgent: make(chan struct{})}
			var mu sync.Mutex
			scopes := map[string]posthog.RequestContext{}
			opts := []server.ServerOption{
				Instrument(posthogmcp.New(queue), WithIdentity(func(_ context.Context, req mcp.CallToolRequest) (Identity, error) {
					return Identity{DistinctID: req.Header.Get("User-Agent")}, nil
				})),
			}
			if test.middlewareCaptures {
				opts = append(opts, server.WithHooks(&server.Hooks{}))
			}
			s := server.NewMCPServer("weather-server", "1.2.3", opts...)
			s.AddTool(weatherTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				userAgent := req.Header.Get("User-Agent")
				<-release[userAgent]
				scope, _ := posthog.RequestContextFromContext(ctx)
				mu.Lock()
				scopes[userAgent] = scope
				mu.Unlock()
				return mcp.NewToolResultText("sunny"), nil
			})
			first := connectHTTPAs(t, s, chatGPTUserAgent, "test-client", server.WithStateLess(true))
			second := connectHTTPAs(t, s, otherUserAgent, "test-client", server.WithStateLess(true))

			firstDone := make(chan error)
			go func() { firstDone <- callWithMeta(t, first, map[string]any{"openai/session": "v1/conv-example-0001"}) }()
			secondDone := make(chan error)
			go func() { secondDone <- callWithMeta(t, second, nil) }()
			close(release[otherUserAgent])
			require.NoError(t, <-secondDone)
			close(release[chatGPTUserAgent])
			require.NoError(t, <-firstDone)

			captures := queue.toolCalls()
			require.Len(t, captures, 2)
			for _, capture := range captures {
				scope := scopes[capture.DistinctId]
				assert.Equal(t, capture.Properties["$session_id"], scope.SessionId, capture.DistinctId)
				assert.Equal(t, capture.DistinctId, scope.DistinctId)
				assert.Equal(t, capture.Properties["$mcp_session_source"], scope.Properties["$mcp_session_source"])
			}
			assert.Equal(t, chatGPTSession, scopes[chatGPTUserAgent].SessionId)
		})
	}
}

func TestAnonymousCustomEventsUseTheCallsDistinctID(t *testing.T) {
	for _, test := range []struct {
		name           string
		hostDistinctID string
	}{
		{name: "the call's session is the distinct id"},
		{name: "a host distinct id is kept", hostDistinctID: "host-user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			host := server.WithToolHandlerMiddleware(func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
				return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return next(posthog.WithRequestContext(ctx, posthog.RequestContext{DistinctId: test.hostDistinctID}), req)
				}
			})
			s := server.NewMCPServer("weather-server", "1.2.3", host, Instrument(posthogmcp.New(queue)))
			var scope posthog.RequestContext
			s.AddTool(weatherTool(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				scope, _ = posthog.RequestContextFromContext(ctx)
				return mcp.NewToolResultText("sunny"), nil
			})
			require.NoError(t, callWithMeta(t, connectInProcess(t, s), nil))

			if test.hostDistinctID != "" {
				assert.Equal(t, test.hostDistinctID, scope.DistinctId)
				assert.NotContains(t, scope.Options, "process_person_profile")
				return
			}
			assert.Equal(t, queue.onlyToolCall(t)["$session_id"], scope.DistinctId)
			assert.Equal(t, false, scope.Options["process_person_profile"])
		})
	}
}
