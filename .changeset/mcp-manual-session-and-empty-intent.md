---
"posthog-go": minor
---

`posthogmcp.CaptureToolCall` follows two rules of the mcp-analytics spec that it skipped. A call with neither a `SessionID` nor a valid `ConversationID` now gets a new `ses_<UUIDv7>` as `$session_id`, and `distinct_id` falls back to it instead of `"anonymous"` (events stay personless without an explicit `DistinctID`). An `Intent` of `{}`, which a client sends for an empty context, is no intent and is omitted.
