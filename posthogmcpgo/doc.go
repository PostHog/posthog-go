// Package posthogmcpgo captures PostHog MCP analytics for servers built with
// github.com/mark3labs/mcp-go.
//
// [Instrument] returns a server option that records every tools/call to a
// registered tool as a $mcp_tool_call event through a [posthogmcp.Analytics],
// with the same properties the posthogmcpsdk adapter and the Python and
// TypeScript SDKs send. A returned error or an isError result the client
// receives, including a schema validation failure, marks the call as failed. Analytics errors and panics never change
// the MCP response.
//
// Like the other SDKs, it advertises a required context argument on each
// tool, in which the agent states why it is calling the tool, and captures it
// as the event's intent. The argument is removed before the tool's handler
// sees the call. [WithContextParameter] turns this off.
//
// This is a separate Go module so that the core posthog-go SDK does not
// depend on mcp-go.
package posthogmcpgo
