---
"posthog-go": minor
---

posthogmcpsdk: group ChatGPT, Codex and VS Code calls by the conversation id each sends in `_meta`, ahead of conversation handles and transport sessions, and send `$mcp_session_source` on every event. ChatGPT's `tools/list` has no `conversation_id` argument and its calls are minted no handle. A tool handler's context now carries the call's session and identity, so custom events captured with `posthog.EnqueueWithContext` land in the call's session.

posthogmcpsdk: a one-client session now lasts across overlapping calls, rotates once 30 minutes or more have passed since its last call ended, and is never evicted while a call runs. Both adapters require core `v2.5.0`, the release this stack's changesets produce.
