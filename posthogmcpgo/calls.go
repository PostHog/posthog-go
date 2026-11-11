package posthogmcpgo

import (
	"container/list"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// maxCallsInFlight bounds the calls remembered at once. mcp-go reports every
// call's result or error, so only a handler panic that a transport recovers
// leaves one behind, and the oldest start is dropped first.
const maxCallsInFlight = 1024

// callsInFlight are the tools/calls mcp-go is handling, keyed by the request
// its call hooks share, oldest start first.
type callsInFlight struct {
	mu      sync.Mutex
	started map[*mcp.CallToolRequest]*list.Element
	order   *list.List
}

// callStart is what a call's capture needs from when it arrived. It is read
// then, since the handler may unregister the tool and the session may close or
// sit past its inactivity timeout before the call ends.
type callStart struct {
	at              time.Time
	protocolVersion string
	sessionID       string
	tool            toolInfo
}

type trackedCall struct {
	req   *mcp.CallToolRequest
	start callStart
}

func newCallsInFlight() *callsInFlight {
	return &callsInFlight{started: map[*mcp.CallToolRequest]*list.Element{}, order: list.New()}
}

func (c *callsInFlight) start(req *mcp.CallToolRequest, call callStart) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.started) >= maxCallsInFlight {
		c.remove(c.order.Front())
	}
	c.started[req] = c.order.PushBack(trackedCall{req: req, start: call})
}

func (c *callsInFlight) finish(req *mcp.CallToolRequest) (callStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.started[req]
	if !ok {
		return callStart{}, false
	}
	return c.remove(element), true
}

func (c *callsInFlight) remove(element *list.Element) callStart {
	call := c.order.Remove(element).(trackedCall)
	delete(c.started, call.req)
	return call.start
}
