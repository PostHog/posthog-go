---
"posthog-go": major
---

`posthogotel.EnqueueWithContext` adds the active span's `$trace_id` and `$span_id` only when neither the event nor the request context sets either key. In posthog-go 1.37.0 and later 1.x.x releases, each derived ID filled on its own, so an event that set only `$trace_id` got the active span's `$span_id`.
