package posthogmcpsdk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/v2/posthogmcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var generatedSessionID = regexp.MustCompile(`^ses_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Expected values come from posthog-js's deterministicPrefixedId.
func TestDeterministicSessionIDMatchesReference(t *testing.T) {
	for input, want := range map[string]string{
		"0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f": "ses_6df45f0102a182bcd5e8dd5dad6c65a0",
		"session-123":                          "ses_346bdc9a6b5cb06913bb476a65021eb5",
		"a":                                    "ses_8601ec8c0eec655f4ec03fd0b1129ba7",
		"":                                     "ses_84222325cbf29ce4805967715e2fab98",
		"MCP session/with spaces":              "ses_f7a86f830acb61f8cea1de7b3d66eefc",
		"é-ünï":                                "ses_f52aeeca2d955f05d88a791a4ead6a71",
	} {
		assert.Equal(t, want, deterministicSessionID(input), "input %q", input)
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

func withClock(clock *fakeClock) Option {
	return func(cfg *config) { cfg.now = clock.Now }
}

func TestGeneratedSessionRotatesAfterInactivity(t *testing.T) {
	queue := &fakeQueue{}
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	server := newServer()
	Instrument(server, posthogmcp.New(queue), withClock(clock))
	addWeatherTool(server, nil)
	client := connectInMemory(t, server)

	steps := []struct {
		name         string
		idleBefore   time.Duration
		sameAsBefore bool
	}{
		{name: "first call", sameAsBefore: false},
		{name: "shortly after", idleBefore: time.Minute, sameAsBefore: true},
		{name: "exactly at the timeout", idleBefore: sessionInactivityTimeout, sameAsBefore: true},
		{name: "activity extends the session", idleBefore: 29 * time.Minute, sameAsBefore: true},
		{name: "past the timeout", idleBefore: sessionInactivityTimeout + time.Second, sameAsBefore: false},
	}
	var previous string
	for i, step := range steps {
		clock.advance(step.idleBefore)
		_, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "weather", Arguments: map[string]any{"city": "Melbourne"}})
		require.NoError(t, err, step.name)

		current, _ := queue.toolCalls()[i].Properties["$session_id"].(string)
		assert.Regexp(t, generatedSessionID, current, step.name)
		if i > 0 {
			assert.Equal(t, step.sameAsBefore, current == previous, step.name)
		}
		previous = current
	}
}

func TestConnectionsToOneServerGetDifferentSessions(t *testing.T) {
	queue := &fakeQueue{}
	server := newServer()
	Instrument(server, posthogmcp.New(queue))
	addWeatherTool(server, nil)
	first, second := connectInMemory(t, server), connectInMemory(t, server)

	var wg sync.WaitGroup
	for _, client := range []*mcpsdk.ClientSession{first, second, first, second} {
		wg.Go(func() {
			_, err := client.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "weather", Arguments: map[string]any{"city": "Melbourne"}})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	distinct := map[any]bool{}
	for _, capture := range queue.toolCalls() {
		distinct[capture.Properties["$session_id"]] = true
	}
	assert.Len(t, distinct, 2)
}

func TestIdleGeneratedSessionsAreEvicted(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	resolver := newSessionResolver(clock.Now)
	idle, active := &mcpsdk.ServerSession{}, &mcpsdk.ServerSession{}
	resolver.resolve(idle, false)
	resolver.resolve(active, false)
	require.Len(t, resolver.generated, 2)

	clock.advance(sessionInactivityTimeout - time.Minute)
	resolver.resolve(active, false)
	clock.advance(2 * time.Minute)
	resolver.resolve(active, false)

	assert.Len(t, resolver.generated, 1)
	assert.Contains(t, resolver.generated, active)
}

func TestRequestScopedSessionsAreNotRetained(t *testing.T) {
	resolver := newSessionResolver(time.Now)
	ids := map[string]bool{}
	for range 10_000 {
		id := resolver.resolve(&mcpsdk.ServerSession{}, true)
		assert.Regexp(t, generatedSessionID, id)
		ids[id] = true
	}

	assert.Len(t, ids, 10_000)
	assert.Empty(t, resolver.generated)
}

func TestGeneratedSessionsAreCappedOldestFirst(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	resolver := newSessionResolver(clock.Now)
	oldest := &mcpsdk.ServerSession{}
	resolver.resolve(oldest, false)
	for range maxGeneratedSessions {
		clock.advance(time.Millisecond)
		resolver.resolve(&mcpsdk.ServerSession{}, false)
	}

	assert.Len(t, resolver.generated, maxGeneratedSessions)
	_, retained := resolver.generated[oldest]
	assert.False(t, retained)
}

// A stateless HTTP request is the one transport whose sessions are request
// scoped, and go-sdk marks it with request Extra and an empty session id.
func TestStatelessHTTPRequestsAreRequestScoped(t *testing.T) {
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"},
		&mcpsdk.ServerOptions{GetSessionID: func() string { return "" }},
	)
	var mu sync.Mutex
	var seen []string
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if call, ok := req.(*mcpsdk.CallToolRequest); ok {
				mu.Lock()
				seen = append(seen, fmt.Sprintf("id=%q extra=%t", call.Session.ID(), call.Extra != nil))
				mu.Unlock()
			}
			return next(ctx, method, req)
		}
	})
	addWeatherTool(server, nil)
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(httpServer.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "weather", Arguments: map[string]any{"city": "Melbourne"}})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{`id="" extra=true`}, seen)
}
