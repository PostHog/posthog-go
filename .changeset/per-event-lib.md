---
"posthog-go": minor
---

Add `Capture.Library` and `Exception.Library`. When set, the value replaces `posthog-go` as that
event's `$lib`, while `$lib_version` stays the posthog-go version and every other event keeps
`posthog-go`. `posthogmcp` uses it, so MCP analytics events now report `$lib` `posthog-go-mcp`.
Capture v1 mode still reports `posthog-go`, because the server reads `$lib` from the per-request
`PostHog-Sdk-Info` header there.
