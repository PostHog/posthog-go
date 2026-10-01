---
"posthog-go": minor
---

`posthogmcpsdk` anchors conversations like the Python and TypeScript SDKs: tools get an optional `conversation_id`
argument, a call with neither a transport session nor a valid handle gets a new UUIDv7 handle in its result, and an
echoed handle sets `$mcp_conversation_id` and the `$session_id` derived from it, so a stateless HTTP client's calls
share one session. Turn it off with `WithConversationID(false)`.
