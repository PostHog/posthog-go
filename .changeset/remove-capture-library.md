---
"posthog-go": major
---

Remove `Capture.Library` and `Exception.Library`. Capture v1 takes `$lib` and `$lib_version` from the per-request `PostHog-Sdk-Info` header, which is always `posthog-go/<version>`, so a per-event library name never reached the backend. `posthogmcp` events now report `posthog-go`. See `docs/migration-v2.md`.
