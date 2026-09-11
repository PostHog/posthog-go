---
"posthog-go": minor
---

Add `Config.MaxRetryBackoff` to cap how long a single capture retry waits. It bounds the default exponential backoff and clamps a server `Retry-After` to the same value; a custom `Config.RetryAfter` is still used as given. Defaults to `DefaultMaxRetryBackoff` (30s), which was previously a hard-coded constant.
