---
"posthog-go": minor
---

`posthogmcpsdk` accepts `WithIntentFallback`, a callback that supplies `$mcp_intent` for tool calls whose agent sent no `context`, with `$mcp_intent_source` of `inferred`. The agent's own `context` still wins.
