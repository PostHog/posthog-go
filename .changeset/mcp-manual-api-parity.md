---
"posthog-go": minor
---

`posthogmcp.ToolCall` gains `ConversationID`, `LLMModel`, `LLMModelSource`, `ClientUserAgent` and `VendorClient`.
The new fields match the events posthog-python captures; `ConversationID` goes beyond its manual `capture_tool_call`
API, which has no such argument. The `$exception` event now carries `$exception_level`, and its exception type is
always the Go type of `Error`, while `$mcp_error_type` is the explicit `ErrorType`, else that type (such as
`fs.PathError`), else `Error`. Typed responses with large images keep their text blocks.
