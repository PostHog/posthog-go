---
"posthog-go": major
---

Remove `Capture.Library` and `Exception.Library`. Capture v1 takes `$lib` and `$lib_version` from the per-request `PostHog-Sdk-Info` header, which is always `posthog-go/<version>`, so a per-event library name cannot reach the backend. In 1.x it reached the backend only on the default `/batch/` path. `posthogmcp` events now report `posthog-go`. See `docs/migration-v2.md`.
