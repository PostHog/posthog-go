---
"posthog-go": minor
---

`posthogmcpsdk` no longer counts a call naming an unregistered tool, or an `input_required` round, as a `$mcp_tool_call`. It sends `$mcp_unknown_tool` and `$mcp_input_required` instead (go-sdk v1.8 for rounds). A legacy-revision call whose handler keeps asking for input after go-sdk's one re-entry is a failed `$mcp_tool_call` with `$mcp_error_type` `input_required`.
