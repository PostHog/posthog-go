---
"posthog-go": patch
---

Clamp legacy `/batch/` retry waits to the same 30s ceiling as analytics v1 capture. An unbounded `Retry-After` header could otherwise block a batch worker until the queue filled.
