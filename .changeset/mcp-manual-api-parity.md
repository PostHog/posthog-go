---
"posthog-go": minor
---

`posthogmcp.ToolCall` gains `ConversationID`, `LLMModel`, `LLMModelSource`, `ClientUserAgent` and `VendorClient`,
matching the manual MCP analytics API of posthog-python. The `$exception` event now carries `$exception_level`,
the default `ErrorType` is the Go type of `Error` (such as `fs.PathError`) instead of `Error`, and typed responses
with large images keep their text blocks.
