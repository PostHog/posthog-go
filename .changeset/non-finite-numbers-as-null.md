---
"posthog-go": patch
---

Send a NaN or ±Inf property value, `float32` or `float64`, as `null` and keep the event, as 1.36.1 and later do, instead of dropping the event. A struct value that holds one is sent as `null` as a whole. Each event is checked as JSON before it is queued, so a custom `MarshalJSON` that returns invalid JSON fails only its own event instead of the request it is in.
