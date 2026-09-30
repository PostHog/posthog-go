package posthogmcpsdk

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

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
	catalog := newToolCatalog(true)
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
			name: "page cap",
			failing: func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				return &mcpsdk.ListToolsResult{NextCursor: "more"}, nil
			},
			wantErr: "posthogmcpsdk: tools/list through inner handler has more than 100 pages",
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
			catalog := newToolCatalog(true)
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
			assert.Equal(t, toolInfo{description: "about tool", contextInjected: true}, info)
			assert.NoError(t, err)
		})
	}
}

type panickingSchema struct{}

func (panickingSchema) MarshalJSON() ([]byte, error) { panic("schema panic") }

func TestCatalogPropagatesItsOwnPanicAndStaysRetryable(t *testing.T) {
	catalog := newToolCatalog(true)
	schema := any(panickingSchema{})
	next := func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		return &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{{Name: "tool", InputSchema: schema}}}, nil
	}

	assert.PanicsWithValue(t, "schema panic", func() { _, _ = catalog.lookup(t.Context(), next, callRequest("tool")) })

	schema = map[string]any{"type": "object"}
	info, err := catalog.lookup(t.Context(), next, callRequest("tool"))
	assert.Equal(t, toolInfo{contextInjected: true}, info)
	assert.NoError(t, err)
}
