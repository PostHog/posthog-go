---
"posthog-go": minor
---

posthogmcp: send `$mcp_session_source` on every MCP event, and read the conversation ids ChatGPT, Codex and VS Code send in `_meta` with `ReadClientConversation`. `ToolCall.SessionSource` and `EventContext.SessionSource` say what chose the session. A client conversation id outranks a conversation handle, and a host session keeps the handle in `$mcp_conversation_id`. `WithCallContext` puts a call's session and identity on the context its handler captures custom events with.
