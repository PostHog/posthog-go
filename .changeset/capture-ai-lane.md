---
"posthog-go": major
---

Add `Client.EnqueueAI`, which sends to PostHog's dedicated AI capture endpoint on an isolated lane with its own queue, batching, retry state, compression, upload timeout and size limits. See `docs/migration-v2.md`.
