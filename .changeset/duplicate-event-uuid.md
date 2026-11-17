---
"posthog-go": patch
---

Report each copy of a repeated event uuid that capture dropped to `Callback.Failure` as a `*CaptureEventError` with `Result` `drop` and `Details` `duplicate_event_uuid`, instead of giving it the first copy's result.
