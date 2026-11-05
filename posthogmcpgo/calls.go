package posthogmcpgo

import (
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// abandonedCallAge is how long a call may go without mcp-go reporting its
// result before it is forgotten. mcp-go reports every call's result or
// error, so only a handler panic that a transport recovers leaves one behind.
const abandonedCallAge = time.Hour

// callsInFlight are the start times of the tools/calls mcp-go is handling,
// keyed by the request its call hooks share.
type callsInFlight struct {
	mu        sync.Mutex
	started   map[*mcp.CallToolRequest]time.Time
	lastSweep time.Time
}

func newCallsInFlight() *callsInFlight {
	return &callsInFlight{started: map[*mcp.CallToolRequest]time.Time{}}
}

func (c *callsInFlight) start(req *mcp.CallToolRequest, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.lastSweep) > abandonedCallAge {
		c.lastSweep = now
		for abandoned, started := range c.started {
			if now.Sub(started) > abandonedCallAge {
				delete(c.started, abandoned)
			}
		}
	}
	c.started[req] = now
}

func (c *callsInFlight) finish(req *mcp.CallToolRequest) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	started, ok := c.started[req]
	delete(c.started, req)
	return started, ok
}
