---
"posthog-go": minor
---

`posthogmcp` adds `CaptureMissingCapability`, which sends `$mcp_missing_capability` for an agent's report of a capability the server lacks, with the report as `$mcp_intent`.
