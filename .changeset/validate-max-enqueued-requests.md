---
"posthog-go": patch
---

Reject negative `MaxEnqueuedRequests` in `Config.Validate` instead of panicking when creating the batches channel.
