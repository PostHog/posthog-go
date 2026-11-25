package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	chatGPTUserAgent = "openai-mcp/1.0.0 (ChatGPT)"
	codexUserAgent   = "codex-mcp-client/0.50.0"
	chatGPTSession   = "ses_2f5d35b7108d17c807766f2f769a1e8c"
	codexSession     = "ses_0fdf126b8e2c1f9689ca12f3dd3383e6"
	vsCodeSession    = "ses_0571acb894e6753d33e37e5c2b327c59"
	codexUUID        = "019a4f2e-1c3d-7b8a-9e0f-1a2b3c4d5e6f"
)

func connectAs(t *testing.T, server *mcpsdk.Server, userAgent, clientName string, stateful bool) *mcpsdk.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{Stateless: !stateful},
	))
	t.Cleanup(httpServer.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: clientName, Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
		Endpoint:   httpServer.URL,
		HTTPClient: &http.Client{Transport: headerTransport{http.Header{"User-Agent": {userAgent}}}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestClientConversationIDs(t *testing.T) {
	const (
		handle    = "handle"
		generated = "generated"
	)
	for _, test := range []struct {
		name         string
		userAgent    string
		clientName   string
		meta         mcpsdk.Meta
		echo         string
		anchoringOff bool
		stateful     bool
		wantSession  string
		wantSource   string
		wantHandle   bool
	}{
		{name: "a chatgpt id sets the session and gets no handle", userAgent: chatGPTUserAgent, meta: mcpsdk.Meta{"openai/session": "v1/conv-example-0001"}, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "a chatgpt id wins over an echoed handle", userAgent: chatGPTUserAgent, meta: mcpsdk.Meta{"openai/session": "v1/conv-example-0001"}, echo: vectorHandle, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "a chatgpt id sets the session with anchoring off", userAgent: chatGPTUserAgent, meta: mcpsdk.Meta{"openai/session": "v1/conv-example-0001"}, anchoringOff: true, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "a chatgpt id wins over a transport session", userAgent: chatGPTUserAgent, meta: mcpsdk.Meta{"openai/session": "v1/conv-example-0001"}, stateful: true, wantSession: chatGPTSession, wantSource: "client_metadata"},
		{name: "an invalid codex id falls back to a handle", userAgent: codexUserAgent, meta: mcpsdk.Meta{"sessionId": "not-a-uuid"}, wantSession: handle, wantSource: "conversation", wantHandle: true},
		{name: "an invalid chatgpt id gets no handle", userAgent: chatGPTUserAgent, meta: mcpsdk.Meta{"openai/session": ""}, wantSession: generated, wantSource: "generated"},
		{name: "chatgpt without its id gets no handle", userAgent: chatGPTUserAgent, wantSession: generated, wantSource: "generated"},
		{name: "a handle chatgpt echoes still counts", userAgent: chatGPTUserAgent, echo: vectorHandle, wantSession: vectorSessionID, wantSource: "conversation"},
		{name: "a codex id sets the session", userAgent: codexUserAgent, meta: mcpsdk.Meta{"sessionId": "019A4F2E-1C3D-7B8A-9E0F-1A2B3C4D5E6F"}, wantSession: codexSession, wantSource: "client_metadata"},
		{name: "a vs code id sets the session by client name", clientName: "Visual Studio Code", stateful: true, meta: mcpsdk.Meta{"vscode.conversationId": "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e"}, wantSession: vsCodeSession, wantSource: "client_metadata"},
		{name: "a key is read only from the client that defines it", userAgent: "example-client/1.0", meta: mcpsdk.Meta{"sessionId": codexUUID, "openai/session": "v1/conv-example-0001"}, wantSession: handle, wantSource: "conversation", wantHandle: true},
		{name: "a transport session", userAgent: "example-client/1.0", stateful: true, wantSession: "transport", wantSource: "transport"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			server := newSessionlessServer()
			if test.stateful {
				server = newServer()
			}
			Instrument(server, posthogmcp.New(queue), WithConversationID(!test.anchoringOff))
			argumentsEchoTool(server, map[string]any{"type": "object"})
			clientName := test.clientName
			if clientName == "" {
				clientName = "http-client"
			}
			session := connectAs(t, server, test.userAgent, clientName, test.stateful)
			_, err := session.ListTools(t.Context(), nil)
			require.NoError(t, err)

			arguments := map[string]any{"query": "flags"}
			if test.echo != "" {
				arguments["conversation_id"] = test.echo
			}
			result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Meta: test.meta, Name: "echo", Arguments: arguments})
			require.NoError(t, err)

			delivered := deliveredHandle(t, result)
			if test.wantHandle {
				require.Regexp(t, conversationHandle, delivered)
			} else {
				assert.Empty(t, delivered, "the result carries no conversation_id block")
			}
			assert.JSONEq(t, `{"query":"flags"}`, result.Content[0].(*mcpsdk.TextContent).Text, "the handler never sees conversation_id")

			properties := queue.onlyToolCall(t)
			assert.Equal(t, test.wantSource, properties["$mcp_session_source"])
			switch test.wantSession {
			case handle:
				assert.Equal(t, deterministicSessionID(delivered), properties["$session_id"])
				assert.Equal(t, delivered, properties["$mcp_conversation_id"])
			case generated:
				assert.Regexp(t, generatedSessionID, properties["$session_id"])
				assert.NotContains(t, properties, "$mcp_conversation_id")
			case "transport":
				assert.Equal(t, deterministicSessionID(session.ID()), properties["$session_id"])
			default:
				assert.Equal(t, test.wantSession, properties["$session_id"])
				if test.wantSource == "client_metadata" {
					assert.NotContains(t, properties, "$mcp_conversation_id")
				}
			}
		})
	}
}

func TestHandleFreeClientsAreNotAdvertisedAConversationID(t *testing.T) {
	for _, test := range []struct {
		name      string
		userAgent string
		want      bool
	}{
		{name: "chatgpt", userAgent: chatGPTUserAgent},
		{name: "codex", userAgent: codexUserAgent, want: true},
		{name: "another client", userAgent: "example-client/1.0", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newSessionlessServer()
			Instrument(server, posthogmcp.New(&fakeQueue{}), WithMissingCapabilityTool(""))
			addWeatherTool(server, nil)
			session := connectAs(t, server, test.userAgent, "http-client", false)
			listing, err := session.ListTools(t.Context(), nil)
			require.NoError(t, err)
			require.Len(t, listing.Tools, 2)
			for _, tool := range listing.Tools {
				var schema struct {
					Properties map[string]any `json:"properties"`
				}
				require.NoError(t, json.Unmarshal([]byte(jsonString(t, tool.InputSchema)), &schema))
				properties := schema.Properties
				_, advertised := properties["conversation_id"]
				assert.Equal(t, test.want, advertised, tool.Name)
				assert.Contains(t, properties, "context", tool.Name)
				if tool.Name == "weather" {
					assert.Equal(t, test.want, strings.Contains(jsonString(t, tool.OutputSchema), `"_mcp_instructions"`), "output schema")
				}
			}
		})
	}
}

func TestCustomEventsKeepTheirCallsSession(t *testing.T) {
	queue := &fakeQueue{}
	server := newSessionlessServer()
	Instrument(server, posthogmcp.New(queue), WithConversationID(false),
		WithIdentity(func(_ context.Context, req *mcpsdk.CallToolRequest) (Identity, error) {
			return Identity{DistinctID: req.Extra.Header.Get("User-Agent")}, nil
		}))
	release := map[string]chan struct{}{chatGPTUserAgent: make(chan struct{}), "example-client/1.0": make(chan struct{})}
	var mu sync.Mutex
	scopes := map[string]posthog.RequestContext{}
	server.AddTool(&mcpsdk.Tool{Name: "search_docs", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			userAgent := req.Extra.Header.Get("User-Agent")
			<-release[userAgent]
			scope, _ := posthog.RequestContextFromContext(ctx)
			mu.Lock()
			scopes[userAgent] = scope
			mu.Unlock()
			return &mcpsdk.CallToolResult{}, nil
		})

	first := connectAs(t, server, chatGPTUserAgent, "http-client", false)
	second := connectAs(t, server, "example-client/1.0", "http-client", false)
	firstDone := make(chan error)
	go func() {
		_, err := first.CallTool(context.Background(), &mcpsdk.CallToolParams{
			Meta: mcpsdk.Meta{"openai/session": "v1/conv-example-0001"},
			Name: "search_docs",
		})
		firstDone <- err
	}()
	secondDone := make(chan error)
	go func() {
		_, err := second.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "search_docs"})
		secondDone <- err
	}()
	close(release["example-client/1.0"])
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
	assert.NotEqual(t, scopes[chatGPTUserAgent].SessionId, scopes["example-client/1.0"].SessionId)
}

func TestHandleFreeClientsGetNoMirroredHandle(t *testing.T) {
	queue := &fakeQueue{}
	server := newSessionlessServer()
	Instrument(server, posthogmcp.New(queue))
	addWeatherTool(server, nil)
	session := connectAs(t, server, chatGPTUserAgent, "http-client", false)
	_, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "weather",
		Arguments: map[string]any{"city": "Melbourne", "conversation_id": vectorHandle},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.JSONEq(t, `{"temperature":21}`, jsonString(t, result.StructuredContent))
	assert.Equal(t, vectorSessionID, queue.onlyToolCall(t)["$session_id"], "the echoed handle still counts")
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
			server := newServer()
			Instrument(server, posthogmcp.New(queue))
			if test.hostDistinctID != "" {
				withRequestContext(server, posthog.RequestContext{DistinctId: test.hostDistinctID})
			}
			var scope posthog.RequestContext
			server.AddTool(&mcpsdk.Tool{Name: "search_docs", InputSchema: map[string]any{"type": "object"}},
				func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					scope, _ = posthog.RequestContextFromContext(ctx)
					return &mcpsdk.CallToolResult{}, nil
				})
			_, err := connectInMemory(t, server).CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "search_docs"})
			require.NoError(t, err)

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

// go-sdk v1.8 and later read a 2026-07-28 request's own clientInfo, which can
// name a different client than the session's initialize did.
func TestClientConversationPrefersTheRequestsClientInfo(t *testing.T) {
	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Meta: mcpsdk.Meta{
		"io.modelcontextprotocol/protocolVersion": "2026-07-28",
		"io.modelcontextprotocol/clientInfo":      map[string]any{"name": "Visual Studio Code", "version": "1.105.0"},
		"vscode.conversationId":                   "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e",
	}}}
	if _, ok := any(req).(interface{ ClientInfo() *mcpsdk.Implementation }); !ok {
		t.Skip("this go-sdk version reads client info only from initialize")
	}
	assert.Equal(t, "Visual Studio Code", clientInfo(req).Name)
	assert.Equal(t, vsCodeSession, clientConversation(req, req.Params.Meta).SessionID)
}
