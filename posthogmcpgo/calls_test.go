package posthogmcpgo

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func TestCallsInFlightAreBoundedByCount(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name      string
		started   int
		firstAge  time.Duration
		wantFirst bool
	}{
		{name: "a call running for hours is kept", started: 2, firstAge: 5 * time.Hour, wantFirst: true},
		{name: "calls up to the cap are kept", started: maxCallsInFlight, firstAge: time.Second, wantFirst: true},
		{name: "past the cap the oldest start is dropped", started: maxCallsInFlight + 1, firstAge: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := newCallsInFlight()
			requests := make([]*mcp.CallToolRequest, test.started)
			for i := range requests {
				requests[i] = &mcp.CallToolRequest{}
				at := now
				if i == 0 {
					at = now.Add(-test.firstAge)
				}
				start := newCallStart(func() {})
				start.at = at
				calls.start(requests[i], arrival{headers: uintptr(i + 1)}, start)
			}

			_, first := calls.finish(requests[0])
			assert.Equal(t, test.wantFirst, first)
			_, last := calls.finish(requests[len(requests)-1])
			assert.True(t, last, "the newest call is kept")
			assert.Equal(t, calls.order.Len(), len(calls.started), "the order and the index agree")
			assert.Equal(t, calls.order.Len(), len(calls.arrivals), "the order and the arrivals agree")
		})
	}
}

func TestEvictingACallEndsIt(t *testing.T) {
	calls := newCallsInFlight()
	var ended atomic.Int32
	for i := range maxCallsInFlight + 1 {
		calls.start(&mcp.CallToolRequest{}, arrival{headers: uintptr(i + 1)}, newCallStart(func() { ended.Add(1) }))
	}
	assert.Equal(t, int32(1), ended.Load(), "the evicted call ended")
}
