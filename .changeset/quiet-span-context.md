---
"posthog-go": minor
---

Add an optional OpenTelemetry context processor and HTTP middleware for session and person IDs on application spans. Both take the PostHog client and add no IDs when that client is nil, disabled, or closed.
