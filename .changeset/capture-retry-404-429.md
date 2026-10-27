---
"posthog-go": patch
---

Capture retries HTTP `404` and `429` like `408` and `500`, `502`, `503`, `504`, with the same backoff and `Retry-After` handling.
