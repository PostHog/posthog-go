---
"posthog-go": minor
---

Unify the capture retry backoff ceiling at 30s. `DefaultBackoff`'s cap changes from 10s to 30s (default only — override via `Config.RetryAfter`), and the Capture V1 send now clamps a server `Retry-After` to the same 30s so a hostile or buggy header cannot park a batch goroutine. `Retry-After` still acts as a minimum; the configured backoff is never truncated. This aligns the default retry behavior with posthog-rs and posthog-python.
