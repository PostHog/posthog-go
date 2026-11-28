---
"posthog-go": minor
---

Stop and resume capture at runtime with `OptOutCapturing`, `OptInCapturing` and `IsOptedOut`. While opted out, `Enqueue`, `EnqueueWithContext`, `EnqueueAI` and `EnqueueAIWithContext` drop the message and return nil, sending nothing; events accepted before the opt-out stay queued and are still delivered. Clients start opted in, and a client constructed without a project API key always reads as opted out. Reach the methods through the new `OptOutController` interface (`client.(posthog.OptOutController)`), so implementations and mocks of `Client` are unaffected.
