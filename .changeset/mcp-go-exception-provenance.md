---
"posthog-go": patch
---

`posthogmcp` now sets `$exception_source: "mcp.tool_call"` on the `$exception` it emits alongside a failed tool call, and strips caller-supplied reserved exception metadata from MCP events.
