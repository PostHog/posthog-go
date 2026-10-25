---
"posthog-go": major
---

`posthogotel.EnqueueWithContext` adds the active span's `$trace_id` and `$span_id` only when neither the event nor the request context sets either key. In 1.37 each derived ID filled on its own, so an event that set only `$trace_id` got the active span's `$span_id`.
