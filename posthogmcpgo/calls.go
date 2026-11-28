package posthogmcpgo

import (
	"container/list"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

// maxCallsInFlight bounds the calls remembered at once. mcp-go reports every
// call's result or error, so only a handler panic that a transport recovers
// leaves one behind, and the oldest start is dropped first.
const maxCallsInFlight = 1024

// callsInFlight are the tools/calls mcp-go is handling, keyed by the request
// its call hooks share, oldest start first.
type callsInFlight struct {
	mu       sync.Mutex
	started  map[*mcp.CallToolRequest]*list.Element
	arrivals map[headersKey]*list.Element
	// requests finds a call whose headers middleware outside Instrument
	// replaced, by its request's Done channel.
	requests map[<-chan struct{}]*list.Element
	order    *list.List
}

// arrival tells a call apart from the others in flight by what mcp-go shares
// between a call's request and the copy its handler gets: the session and the
// headers map, which mcp-go makes for each message, with or without HTTP, and
// the Done channel of the context mcp-go makes for each message, which a
// handler's context shares unless middleware outside Instrument adds a
// deadline. Either finds the call, so middleware may replace one of them.
type arrival struct {
	headers headersKey
	done    <-chan struct{}
}

type headersKey struct {
	session server.ClientSession
	headers uintptr
}

func arrivalOf(ctx context.Context, req mcp.CallToolRequest) arrival {
	return arrival{
		headers: headersKey{session: server.ClientSessionFromContext(ctx), headers: reflect.ValueOf(req.Header).Pointer()},
		done:    ctx.Done(),
	}
}

// callStart is what a call's capture needs from when it arrived. It is read
// then, since the handler may unregister the tool and the session may close or
// sit past its inactivity timeout before the call ends.
type callStart struct {
	at           time.Time
	client       posthogmcp.Client
	session      callSession
	identity     Identity
	tool         toolInfo
	conversation conversation
	// end finishes the call's session, once: from the handler middleware when
	// the handler returns or panics, else from the hooks for a call the
	// handler never ran, such as one that failed validation.
	end func()
}

func newCallStart(end func()) callStart {
	var once sync.Once
	return callStart{end: func() { once.Do(end) }}
}

type conversation struct {
	id     string
	minted bool
}

// received is result without the handle block the instrumentation appended,
// and whether the client received the handle minted for this call. Output
// validation or middleware outside Instrument can still replace the result
// after the instrumentation appends the block.
func (call callStart) received(result *mcp.CallToolResult) (*mcp.CallToolResult, bool) {
	if !call.conversation.minted || result == nil {
		return result, false
	}
	block := call.handleBlock()
	for i, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok && text.Text == block {
			returned := *result
			returned.Content = slices.Delete(slices.Clone(result.Content), i, i+1)
			return &returned, true
		}
	}
	return result, false
}

// recordedHandle is the call's handle as its events record it: an echoed one,
// or a minted one the client received.
func (call callStart) recordedHandle(delivered bool) string {
	if call.conversation.minted && !delivered {
		return ""
	}
	return call.conversation.id
}

func (call callStart) deliver(result *mcp.CallToolResult, err error) *mcp.CallToolResult {
	if !call.conversation.minted || err != nil || result == nil {
		return result
	}
	delivered := *result
	delivered.Content = append(slices.Clip(delivered.Content), mcp.NewTextContent(call.handleBlock()))
	return &delivered
}

func (call callStart) handleBlock() string {
	block, _ := json.Marshal(map[string]string{conversationArgument: call.conversation.id})
	return string(block)
}

type callSession struct {
	id     string
	source posthogmcp.SessionSource
}

// A handle minted for this call counts as delivered: that is known only once
// the handler returns, and the host's client sends the custom event at once.
func (call callStart) scoped(ctx context.Context) context.Context {
	session := call.session
	if session.source != posthogmcp.SessionSourceClientMetadata && call.conversation.id != "" {
		session = callSession{id: posthogmcp.ConversationSessionID(call.conversation.id), source: posthogmcp.SessionSourceConversation}
	}
	return posthogmcp.WithCallContext(ctx, session.id, session.source, call.identity.DistinctID)
}

type trackedCall struct {
	req     *mcp.CallToolRequest
	arrival arrival
	start   callStart
}

func newCallsInFlight() *callsInFlight {
	return &callsInFlight{
		started:  map[*mcp.CallToolRequest]*list.Element{},
		arrivals: map[headersKey]*list.Element{},
		requests: map[<-chan struct{}]*list.Element{},
		order:    list.New(),
	}
}

func (c *callsInFlight) start(req *mcp.CallToolRequest, arrived arrival, call callStart) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.started) >= maxCallsInFlight {
		c.remove(c.order.Front())
	}
	element := c.order.PushBack(trackedCall{req: req, arrival: arrived, start: call})
	c.started[req] = element
	c.arrivals[arrived.headers] = element
	if arrived.done != nil {
		c.requests[arrived.done] = element
	}
}

func (c *callsInFlight) startOf(arrived arrival) (callStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.arrivals[arrived.headers]
	if !ok && arrived.done != nil {
		element, ok = c.requests[arrived.done]
	}
	if !ok {
		return callStart{}, false
	}
	return element.Value.(trackedCall).start, true
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
	if c.arrivals[call.arrival.headers] == element {
		delete(c.arrivals, call.arrival.headers)
	}
	if c.requests[call.arrival.done] == element {
		delete(c.requests, call.arrival.done)
	}
	return call.start
}
