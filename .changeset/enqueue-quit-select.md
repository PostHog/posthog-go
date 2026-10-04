---
"posthog-go": patch
---

Return `ErrClosed` from `Enqueue` when shutdown has started and the batch loop has signaled quit, instead of accepting the message until the msgs channel closes.
