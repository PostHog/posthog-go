---
"posthog-go": minor
---

Add the `posthogmcpsdk` module: one-line MCP analytics for servers built on the official Go MCP SDK
(`github.com/modelcontextprotocol/go-sdk`). It captures every `tools/call` as `$mcp_tool_call`, with
intent from an injected `context` argument, like the Python and TypeScript SDKs.
