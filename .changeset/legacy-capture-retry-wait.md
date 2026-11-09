---
"posthog-go": patch
---

Stop the legacy `/batch/` sender from sleeping after its last failed attempt, and clamp its `Retry-After` to the same 30s ceiling as analytics v1 capture.
