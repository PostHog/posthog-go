package posthogmcpgo

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/v2/posthogmcp"
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

// TestGeneratedSessionSpansALongCall resolves a one-client session when a call
// arrives, records the call's completion, and resolves the next call.
func TestGeneratedSessionSpansALongCall(t *testing.T) {
	for _, test := range []struct {
		name         string
		completedAt  time.Duration
		nextArriveAt time.Duration
		wantSame     bool
	}{
		{name: "a call that runs past the timeout keeps its session", completedAt: 40 * time.Minute, nextArriveAt: 41 * time.Minute, wantSame: true},
		{name: "inactivity after a completed call rotates the session", completedAt: time.Minute, nextArriveAt: 32 * time.Minute},
		{name: "a call arriving while another runs past the timeout keeps its session", completedAt: 50 * time.Minute, nextArriveAt: 35 * time.Minute, wantSame: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := newSessionResolver()
			session := server.NewInProcessSession("in-process", nil)
			arrived := time.Now()

			first, entry := sessions.resolve(session, arrived)
			if test.nextArriveAt > test.completedAt {
				sessions.finish(entry, arrived.Add(test.completedAt))
			}
			next, _ := sessions.resolve(session, arrived.Add(test.nextArriveAt))

			assert.Equal(t, test.wantSame, first == next)
		})
	}
}

// TestEvictedSessionsOldCallDoesNotEndItsReplacementsCall evicts a running
// call's session entry, resolves a replacement on the same connection, and ends
// the old call.
func TestEvictedSessionsOldCallDoesNotEndItsReplacementsCall(t *testing.T) {
	for _, test := range []struct {
		name            string
		endReplacement  bool
		wantSameSession bool
	}{
		{name: "the replacement still runs when the old call ends", wantSameSession: true},
		{name: "the replacement expires once it ends too", endReplacement: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := newSessionResolver()
			start := time.Now()
			evicted := server.NewInProcessSession("evicted", nil)
			_, oldEntry := sessions.resolve(evicted, start)
			for i := range maxGeneratedSessions {
				sessions.resolve(server.NewInProcessSession(fmt.Sprint(i), nil), start.Add(time.Second))
			}
			replacement, replacementEntry := sessions.resolve(evicted, start.Add(2*time.Second))

			sessions.finish(oldEntry, start.Add(3*time.Second))
			if test.endReplacement {
				sessions.finish(replacementEntry, start.Add(3*time.Second))
			}
			next, _ := sessions.resolve(evicted, start.Add(sessionInactivityTimeout+4*time.Second))

			assert.Equal(t, test.wantSameSession, replacement == next)
		})
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// mcp-go's stdio worker recovers a handler's panic, so neither call hook
// runs, and the call must still end.
func TestAPanickingCallStillEndsItsSession(t *testing.T) {
	queue := &fakeQueue{}
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	s := server.NewMCPServer("weather-server", "1.2.3",
		Instrument(posthogmcp.New(queue), func(cfg *config) { cfg.now = clock.Now }))
	var calls atomic.Int32
	s.AddTool(weatherTool(), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return mcp.NewToolResultText("sunny"), nil
	})
	c := connectStdio(t, s)

	// The recovered panic is answered without the request's id, so the client
	// never sees it.
	timeout, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	panicking := mcp.CallToolRequest{}
	panicking.Params.Name = "weather"
	panicking.Params.Arguments = map[string]any{"city": "Melbourne"}
	_, err := c.CallTool(timeout, panicking)
	require.Error(t, err)
	for range 2 {
		clock.advance(sessionInactivityTimeout + time.Minute)
		_, err = callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
		require.NoError(t, err)
	}

	captures := queue.toolCalls()
	require.Len(t, captures, 2)
	assert.NotEqual(t, captures[0].Properties["$session_id"], captures[1].Properties["$session_id"],
		"a call after the timeout starts a new session")
}

// server.WithRecovery outside Instrument turns a handler's panic into an
// error the call hooks see, so the call is still captured, and ended once.
func TestAPanicRecoveredOutsideInstrumentIsCaptured(t *testing.T) {
	queue := &fakeQueue{}
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	s := server.NewMCPServer("weather-server", "1.2.3", server.WithRecovery(),
		Instrument(posthogmcp.New(queue), func(cfg *config) { cfg.now = clock.Now }))
	var calls atomic.Int32
	s.AddTool(weatherTool(), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return mcp.NewToolResultText("sunny"), nil
	})
	c := connectInProcess(t, s)

	_, err := callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
	require.Error(t, err)
	clock.advance(sessionInactivityTimeout + time.Minute)
	_, err = callTool(t, c, "weather", map[string]any{"city": "Melbourne"})
	require.NoError(t, err)

	captures := queue.toolCalls()
	require.Len(t, captures, 2, "the recovered panic is a failed call")
	assert.Equal(t, true, captures[0].Properties["$mcp_is_error"])
	assert.NotEqual(t, captures[0].Properties["$session_id"], captures[1].Properties["$session_id"],
		"the failed call ended, so the next one past the timeout rotates")
}
