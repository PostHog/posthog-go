---
"posthog-go": patch
---

Event `timestamp`, batch `created_at` and the `PostHog-Request-Timestamp` header always carry 3, 6 or 9 fractional-second digits in UTC, for example `2025-01-02T03:04:05.000Z`. Before, Go trimmed trailing zeros, so a whole second had no fraction. Finer precision from your timestamp or the clock is kept.
