---
"posthog-go": minor
---

`posthogmcp.ToolCall` gains `ConversationID`, `LLMModel`, `LLMModelSource`, `ClientUserAgent` and `VendorClient`.
The new fields match the events posthog-python captures; `ConversationID` goes beyond its manual `capture_tool_call`
API, which has no such argument. `ConversationID` is kept only when it is a UUIDv7, lowercased, and it derives
`$session_id` when `SessionID` is empty. `ClientUserAgent` and `VendorClient` have credentials redacted. The
`$exception` event now carries `$exception_level`, which custom properties cannot override, and its exception type
is always the Go type of `Error`, while `$mcp_error_type` is the explicit `ErrorType`, else that type (such as
`fs.PathError`), else `Error`. Typed responses with large images keep their text blocks.
