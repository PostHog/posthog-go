---
"posthog-go": minor
---

Add `posthogmcp.WithCaptureErrorMessages(false)`, which keeps a failed MCP tool call's error text out of `$mcp_error_message` and the `$exception` event. Tool errors often repeat their arguments, and only credentials are redacted from them. The failure is still recorded with its error type, and both carry `Tool <name> returned an error` instead. It applies to the manual API and to the `posthogmcpsdk` and `posthogmcpgo` adapters.
