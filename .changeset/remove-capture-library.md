---
"posthog-go": major
---

Remove `Capture.Library` and `Exception.Library`. Set `$lib` in the event's `Properties` instead, and set `$lib_version` with it: the SDK adds its own version only to its own `$lib`. `posthogmcp` events report `posthog-go-mcp` with the SDK version. See `docs/migration-v2.md`.
