---
"posthog-go": patch
---

Emit `$is_server` on identify and group-identify events too (was only on capture); server-side events are now consistently identifiable.
