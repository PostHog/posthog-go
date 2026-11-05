---
"posthog-go": minor
---

Add `posthogmcpgo`, MCP analytics for servers built on `github.com/mark3labs/mcp-go`. `posthogmcpgo.Instrument` returns one `server.ServerOption` that captures `$mcp_tool_call` events with the same properties as `posthogmcpsdk`, including the agent's intent from an added `context` argument. Tools whose input schema disallows other properties get no added arguments, so servers using `WithInputSchemaValidation` keep working.
