---
"posthog-go": patch
---

Fix a data race between `Enqueue` and `Close`, and report capture failures that previously reached no `Callback` as one aggregate log line per batch.
