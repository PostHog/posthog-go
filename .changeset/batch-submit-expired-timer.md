---
"posthog-go": patch
---

Stop dropping a capture batch when its submit timeout has already expired but the batch queue still has room.
