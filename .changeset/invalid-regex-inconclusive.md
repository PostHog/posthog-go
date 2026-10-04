---
"posthog-go": patch
---

Treat an invalid regex filter pattern as an inconclusive local property match instead of a silent non-match, so evaluation can fall through to later condition groups or remote fallback.
