package posthogmcpgo

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeterministicSessionIDMatchesTheOtherSDKs(t *testing.T) {
	for input, want := range map[string]string{
		"0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f": "ses_6df45f0102a182bcd5e8dd5dad6c65a0",
		"session-123":                          "ses_346bdc9a6b5cb06913bb476a65021eb5",
		"":                                     "ses_84222325cbf29ce4805967715e2fab98",
		"é-ünï":                                "ses_f52aeeca2d955f05d88a791a4ead6a71",
	} {
		assert.Equal(t, want, deterministicSessionID(input), "input %q", input)
	}
}

func TestSessionIDPerTransport(t *testing.T) {
	var sseSessionID atomic.Value
	for _, test := range []struct {
		name    string
		connect func(t *testing.T, s *server.MCPServer) *client.Client
		// want checks the $session_id of two calls from one client.
		want func(t *testing.T, c *client.Client, first, second any)
	}{
		{
			name:    "a one-client connection keeps one generated session",
			connect: connectInProcess,
			want: func(t *testing.T, _ *client.Client, first, second any) {
				assert.Regexp(t, generatedSessionID, first)
				assert.Equal(t, first, second)
			},
		},
		{
			name:    "stdio keeps one generated session",
			connect: connectStdio,
			want: func(t *testing.T, _ *client.Client, first, second any) {
				assert.Regexp(t, generatedSessionID, first)
				assert.Equal(t, first, second)
			},
		},
		{
			name: "SSE derives the session from its transport session id",
			connect: func(t *testing.T, s *server.MCPServer) *client.Client {
				s.GetHooks().AddOnRegisterSession(func(_ context.Context, session server.ClientSession) {
					sseSessionID.Store(session.SessionID())
				})
				return connectSSE(t, s)
			},
			want: func(t *testing.T, _ *client.Client, first, second any) {
				require.NotEmpty(t, sseSessionID.Load())
				assert.Equal(t, deterministicSessionID(sseSessionID.Load().(string)), first)
				assert.Equal(t, first, second)
			},
		},
		{
			name:    "stateful streamable HTTP derives the session from Mcp-Session-Id",
			connect: connectHTTP(),
			want: func(t *testing.T, c *client.Client, first, second any) {
				require.NotEmpty(t, c.GetSessionId())
				assert.Equal(t, deterministicSessionID(c.GetSessionId()), first)
				assert.Equal(t, first, second)
			},
		},
		{
			name:    "stateless streamable HTTP gets a fresh session per call",
			connect: connectHTTP(server.WithStateLess(true)),
			want: func(t *testing.T, _ *client.Client, first, second any) {
				assert.Regexp(t, generatedSessionID, first)
				assert.Regexp(t, generatedSessionID, second)
				assert.NotEqual(t, first, second)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{}
			c := test.connect(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{}))

			for range 2 {
				_, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
				require.NoError(t, err)
			}

			captures := queue.toolCalls()
			require.Len(t, captures, 2)
			test.want(t, c, captures[0].Properties["$session_id"], captures[1].Properties["$session_id"])
		})
	}
}

func TestStreamableHTTPCapturesClientAndHeaders(t *testing.T) {
	queue := &fakeQueue{}
	c := connectHTTP()(t, newInstrumentedServer(queue, weatherTool(), &weatherHandler{}))

	_, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
	require.NoError(t, err)

	properties := queue.onlyToolCall(t)
	assert.Equal(t, "test-client", properties["$mcp_client_name"])
	assert.Equal(t, "2.0.0", properties["$mcp_client_version"])
	assert.Equal(t, mcp.LATEST_PROTOCOL_VERSION, properties["$mcp_protocol_version"])
	assert.NotEmpty(t, properties["$mcp_client_user_agent"])
}

func connectStdio(t *testing.T, s *server.MCPServer) *client.Client {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.NewStdioServer(s).Listen(ctx, serverIn, serverOut)
	}()
	t.Cleanup(func() {
		cancel()
		_ = clientOut.Close()
		<-done
	})
	return initialize(t, client.NewClient(transport.NewIO(clientIn, clientOut, nil)))
}

func connectHTTP(opts ...server.StreamableHTTPOption) func(t *testing.T, s *server.MCPServer) *client.Client {
	return func(t *testing.T, s *server.MCPServer) *client.Client {
		t.Helper()
		httpServer := server.NewTestStreamableHTTPServer(s, opts...)
		t.Cleanup(httpServer.Close)
		c, err := client.NewStreamableHttpClient(httpServer.URL + "/mcp")
		require.NoError(t, err)
		return initialize(t, c)
	}
}
