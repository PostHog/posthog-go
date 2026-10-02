package posthogmcpsdk

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func callRequest(name string) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Session: &mcpsdk.ServerSession{}, Params: &mcpsdk.CallToolParamsRaw{Name: name}}
}

func listing(names ...string) *mcpsdk.ListToolsResult {
	tools := make([]*mcpsdk.Tool, len(names))
	for i, name := range names {
		tools[i] = &mcpsdk.Tool{Name: name, Description: "about " + name, InputSchema: map[string]any{"type": "object"}}
	}
	return &mcpsdk.ListToolsResult{Tools: tools}
}

func TestCatalogListingDuringWalkDoesNotStartAnother(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	walkStarted := make(chan struct{})
	release := make(chan struct{})
	var lists atomic.Int32
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		if lists.Add(1) == 1 {
			close(walkStarted)
			<-release
		}
		return listing("known"), nil
	}

	var lookups sync.WaitGroup
	lookups.Go(func() { _, _ = catalog.lookup(t.Context(), next, callRequest("first")) })
	<-walkStarted
	catalog.advertise(catalog.generation(), listing("known").Tools)
	lookups.Go(func() { _, _ = catalog.lookup(t.Context(), next, callRequest("second")) })
	close(release)
	lookups.Wait()

	assert.Equal(t, int32(1), lists.Load())
}

func TestCatalogRetriesAFailedWalk(t *testing.T) {
	for _, test := range []struct {
		name    string
		failing mcpsdk.MethodHandler
		wantErr string
	}{
		{
			name: "inner error",
			failing: func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				return nil, errors.New("transient")
			},
			wantErr: "posthogmcpsdk: tools/list through inner handler: transient",
		},
		{
			name: "unexpected result type",
			failing: func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				return &mcpsdk.CallToolResult{}, nil
			},
			wantErr: "posthogmcpsdk: tools/list through inner handler returned *mcp.CallToolResult",
		},
		{
			name: "inner panic",
			failing: func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				panic("inner tools/list panic")
			},
			wantErr: "posthogmcpsdk: tools/list through inner handler panicked (string)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
			var failing atomic.Bool
			failing.Store(true)
			next := func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
				if failing.Load() {
					return test.failing(ctx, method, req)
				}
				return listing("tool"), nil
			}

			info, err := catalog.lookup(t.Context(), next, callRequest("tool"))
			assert.Equal(t, toolInfo{}, info)
			assert.EqualError(t, err, test.wantErr)

			failing.Store(false)
			info, err = catalog.lookup(t.Context(), next, callRequest("tool"))
			assert.Equal(t, toolInfo{description: "about tool", injected: []string{"context"}}, info)
			assert.NoError(t, err)
		})
	}
}

// More pages will not appear on a retry, so a capped walk is remembered like a
// complete one; otherwise every unknown-tool call would list 100 pages again.
func TestCatalogRemembersACappedWalk(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	var lists atomic.Int32
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		lists.Add(1)
		return &mcpsdk.ListToolsResult{NextCursor: "more"}, nil
	}

	_, err := catalog.lookup(t.Context(), next, callRequest("missing"))
	assert.EqualError(t, err, "posthogmcpsdk: tools/list through inner handler has more than 100 pages")
	_, err = catalog.lookup(t.Context(), next, callRequest("missing"))
	assert.NoError(t, err)
	assert.Equal(t, int32(maxListingPages), lists.Load())
}

type panickingSchema struct{}

func (panickingSchema) MarshalJSON() ([]byte, error) { panic("schema panic") }

func TestCatalogPropagatesItsOwnPanicAndStaysRetryable(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	schema := any(panickingSchema{})
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		return &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{{Name: "tool", InputSchema: schema}}}, nil
	}

	assert.PanicsWithValue(t, "schema panic", func() { _, _ = catalog.lookup(t.Context(), next, callRequest("tool")) })

	schema = map[string]any{"type": "object"}
	info, err := catalog.lookup(t.Context(), next, callRequest("tool"))
	assert.Equal(t, toolInfo{injected: []string{"context"}}, info)
	assert.NoError(t, err)
}

func TestCatalogForgetsMissesAfterTenSeconds(t *testing.T) {
	for _, test := range []struct {
		name      string
		elapsed   time.Duration
		wantLists int32
		wantInfo  toolInfo
	}{
		{name: "just before", elapsed: 10*time.Second - time.Nanosecond, wantLists: 1, wantInfo: toolInfo{}},
		{name: "at ten seconds", elapsed: 10 * time.Second, wantLists: 2, wantInfo: toolInfo{description: "about added", injected: []string{"context"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
			now := time.Unix(1_700_000_000, 0)
			catalog.now = func() time.Time { return now }
			registered := listing()
			var lists atomic.Int32
			next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				lists.Add(1)
				return registered, nil
			}
			_, _ = catalog.lookup(t.Context(), next, callRequest("added"))

			registered = listing("added")
			now = now.Add(test.elapsed)
			info, err := catalog.lookup(t.Context(), next, callRequest("added"))

			assert.NoError(t, err)
			assert.Equal(t, test.wantInfo, info)
			assert.Equal(t, test.wantLists, lists.Load())
		})
	}
}

func TestCatalogRelearnsKnownToolsAfterTenSeconds(t *testing.T) {
	for _, test := range []struct {
		name      string
		elapsed   time.Duration
		wantLists int32
		wantInfo  toolInfo
	}{
		{name: "just before", elapsed: 10*time.Second - time.Nanosecond, wantLists: 0, wantInfo: toolInfo{description: "about plan", injected: []string{"context"}}},
		{name: "at ten seconds", elapsed: 10 * time.Second, wantLists: 1, wantInfo: toolInfo{description: "replaced"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
			now := time.Unix(1_700_000_000, 0)
			catalog.now = func() time.Time { return now }
			catalog.advertise(catalog.generation(), listing("plan").Tools)

			var lists atomic.Int32
			next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				lists.Add(1)
				return &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{{
					Name:        "plan",
					Description: "replaced",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{"context": map[string]any{"type": "string"}}},
				}}}, nil
			}
			now = now.Add(test.elapsed)
			info, err := catalog.lookup(t.Context(), next, callRequest("plan"))

			assert.NoError(t, err)
			assert.Equal(t, test.wantInfo, info)
			assert.Equal(t, test.wantLists, lists.Load())
		})
	}
}

func TestCatalogListingKeepsRememberedMisses(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	var lists atomic.Int32
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		lists.Add(1)
		return listing("known"), nil
	}

	for range 3 {
		_, _ = catalog.lookup(t.Context(), next, callRequest("removed"))
		catalog.advertise(catalog.generation(), listing("known").Tools)
	}

	assert.Equal(t, int32(1), lists.Load())
}

func TestCatalogWalkInvalidatedMidwayLearnsTheCurrentGeneration(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	var lists atomic.Int32
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		if lists.Add(1) == 1 {
			catalog.invalidate()
		}
		return listing("a"), nil
	}

	info, err := catalog.lookup(t.Context(), next, callRequest("a"))

	assert.NoError(t, err)
	assert.Equal(t, toolInfo{description: "about a", injected: []string{"context"}}, info)
}

func TestCatalogCancelledWalkIsNotReportedAndStaysRetryable(t *testing.T) {
	catalog := newToolCatalog([]analyticsArgument{contextParameter}, time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	cancelling := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		cancel()
		return nil, context.Canceled
	}
	_, err := catalog.lookup(ctx, cancelling, callRequest("a"))
	assert.NoError(t, err)

	succeeding := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		return listing("a"), nil
	}
	info, err := catalog.lookup(t.Context(), succeeding, callRequest("a"))
	assert.NoError(t, err)
	assert.Equal(t, "about a", info.description)
}
