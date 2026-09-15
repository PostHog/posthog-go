---
"posthog-go": major
---

Add `Client.EnqueueAI` for PostHog's dedicated AI capture endpoint (`/i/v1/ai/events`).

- AI events go through an isolated second pipeline with its own queue, batching and retry state, started lazily on the first `EnqueueAI` so clients that never send AI events pay nothing. Routing is by method, never by event name: `Enqueue` never reroutes an `$ai_`-prefixed event.

  The AI endpoint accepts events up to 8MiB, far above the analytics limit, so a shared lane would either reject large AI events or loosen the analytics limit for everything.

- `EnqueueAIWithContext(ctx, client, msg)` is the request-context-aware form, matching `EnqueueWithContext`.

- `Config.CaptureAICompression`, `Config.CaptureAIMaxQueueSize` (default `DefaultCaptureAIMaxQueueSize`, 1000) and `Config.CaptureAIBatchUploadTimeout` (default `DefaultCaptureAIBatchUploadTimeout`, 30s) configure the AI lane independently. Its per-event (8MiB) and per-batch (5MiB) byte limits track the server's and are not configurable.

  The AI lane gets a longer upload timeout than the analytics lane's `BatchUploadTimeout` because its batches are an order of magnitude larger. The two lanes share one HTTP transport, so the connection pool is still shared.

- `CaptureEventError` and `CaptureRequestError` gained `Endpoint`, so a single `Callback` can tell the two lanes apart.

- Breaking for implementers only: `Client` gained `EnqueueAI`, so a hand-written mock of the interface must add it. Callers are unaffected.
