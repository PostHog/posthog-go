---
"posthog-go": major
---

Add an `Options` field to every message type for per-event capture options, such as `process_person_profile`. The SDK sends options unchanged and PostHog validates them, on both `Enqueue` and `EnqueueAI`. Legacy option properties still work, but an option overrides its legacy property. Use options. See `docs/migration-v2.md`.
