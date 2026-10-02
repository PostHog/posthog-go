---
"posthog-go": minor
---

`posthogmcpsdk` can advertise an opt-in `get_more_tools` virtual tool with `WithMissingCapabilityTool`. Calling it sends `$mcp_missing_capability`, with the agent's report as `$mcp_intent`, and the middleware answers the call itself.
