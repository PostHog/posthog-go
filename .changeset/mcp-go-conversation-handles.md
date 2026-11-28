---
"posthog-go": minor
---

posthogmcpgo: anchor stateless conversations with a `conversation_id` handle, as posthogmcpsdk does, so one agent conversation over stateless streamable HTTP stays in one `$session_id`. Turn it off with `WithConversationID(false)`. ChatGPT, which names its conversation in `_meta`, is advertised no `conversation_id` and minted no handle.
