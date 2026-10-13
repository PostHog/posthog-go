---
"posthog-go": patch
---

Send `PostHog-Request-Timestamp` and `created_at` with sub-second precision. Whole seconds made PostHog's clock-skew correction move events up to a second later. The batch debug log no longer prints each message.
