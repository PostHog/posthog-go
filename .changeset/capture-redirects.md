---
"posthog-go": major
---

Capture requests follow only `307` and `308` redirects to the same scheme, host and port as `Endpoint`, at most 5 times, with the same body and headers. Any other redirect fails the batch without a retry, with a `CaptureRequestError` that carries the redirect's status. In 1.x the SDK followed up to 10 redirects of any kind to any host. See `docs/migration-v2.md`.
