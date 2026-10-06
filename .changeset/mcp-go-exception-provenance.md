---
"posthog-go": patch
---

`posthogmcp` now sets `$exception_source: "mcp.tool_call"` on the `$exception` it emits alongside a failed tool call, and reports `mechanism.synthetic` as `false` for a thrown error instead of always `true`.
