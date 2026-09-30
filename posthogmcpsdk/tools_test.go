package posthogmcpsdk

import (
	"context"
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
