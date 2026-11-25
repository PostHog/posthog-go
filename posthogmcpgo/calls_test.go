package posthogmcpgo

import (
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
				calls.start(requests[i], callStart{at: at})
			}

			_, first := calls.finish(requests[0])
			assert.Equal(t, test.wantFirst, first)
			_, last := calls.finish(requests[len(requests)-1])
			assert.True(t, last, "the newest call is kept")
			assert.Equal(t, calls.order.Len(), len(calls.started), "the order and the index agree")
		})
	}
}
