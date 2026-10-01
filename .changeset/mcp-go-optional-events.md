---
"posthog-go": minor
---

`posthogmcp` gains `CaptureUnknownTool` and `CaptureInputRequired` to send `$mcp_unknown_tool` and `$mcp_input_required` from a manual integration. A call that names an unregistered tool, and an `input_required` round the client receives, are not tool calls, so they have their own events.
