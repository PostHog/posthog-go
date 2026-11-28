---
"posthog-go": minor
---

posthogmcpgo: group ChatGPT, Codex and VS Code calls by the conversation id each sends in `_meta`, and send `$mcp_session_source` on every event. A tool handler's context now carries the call's session and identity, so custom events captured with `posthog.EnqueueWithContext` land in the call's session.
