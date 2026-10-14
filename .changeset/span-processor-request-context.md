---
"posthog-go": minor
---

The `otel` `SpanProcessor` now adds the PostHog request context from each span's parent `context.Context` (set with `posthog.WithRequestContext` or `posthog.NewRequestContextMiddleware`) to the AI spans it exports: distinct ID, session ID and properties. Attributes already on a span are kept. With `NewRequestContextMiddleware`, request metadata such as `$ip` and `$user_agent` now appears on the AI spans exported to PostHog. The `otel` module now depends on the core module.
