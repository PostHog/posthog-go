// Package posthogmcpsdk captures PostHog MCP analytics for servers built with
// the official Go MCP SDK, github.com/modelcontextprotocol/go-sdk.
//
// [Instrument] adds receiving middleware that records every tools/call as a
// $mcp_tool_call event through a [posthogmcp.Analytics]: the JSON-RPC request
// as parameters, the result as the response, the duration, the session and
// client from the MCP session, and the tool's description and _meta.category
// from its tools/list entry. An isError result or a returned error marks the
// call as failed. Analytics errors and panics never change the MCP response.
//
// Like the Python and TypeScript SDKs, the middleware advertises a required
// context argument on each tool, in which the agent states why it is calling
// the tool, and captures it as the event's intent. The argument is removed
// before the tool's handler and input validation see the call.
// [WithContextParameter] turns this off.
//
// This is a separate Go module so that the core posthog-go SDK does not
// depend on the MCP SDK.
package posthogmcpsdk
