---
"posthog-go": major
---

Add an `Options` field to every message type for per-event capture options, such as `process_person_profile`. The SDK sends options unchanged and PostHog validates them, on both `Enqueue` and `EnqueueAI`. The legacy `$process_person_profile`, `$cookieless_mode`, `$ignore_sent_at` and `$product_tour_id` properties are moved into their options without conversion; an option that is already set wins. See `docs/migration-v2.md`.
