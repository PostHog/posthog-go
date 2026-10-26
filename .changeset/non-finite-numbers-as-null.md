---
"posthog-go": patch
---

Send a `float64` NaN or ±Inf property value as `null` and keep the event, as 1.36.1 and later do, instead of dropping the event. A struct value that holds one is sent as `null` as a whole. Only an event that holds such a value takes the slower serialization path, so other events cost nothing extra.
