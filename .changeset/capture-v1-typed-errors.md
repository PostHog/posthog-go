---
"posthog-go": minor
---

Enrich capture-v1 failure reporting with typed errors and verbose result logging. When `CaptureMode` is `CaptureModeAnalyticsV1`, `Callback.Failure` now receives either a `*CaptureEventError` (a single event the server dropped, or one still asking to retry once attempts are exhausted — exposing `EventUUID`, `Result`, `Details`, and `Exhausted`) or a `*CaptureRequestError` (a whole request that failed on a non-2xx status, transport error, or malformed body — exposing `StatusCode`, `Code`, `Description`, and unwrapping to the underlying error). Inspect them with `errors.As`. Additionally, with `Verbose: true` the SDK logs one debug line per 2xx response summarizing the per-event directive counts (ok/warning/drop/retry/other) so partial-submission outcomes are easy to debug. The legacy path and its callback errors are unchanged.
