---
"posthog-go": patch
---

Use a dedicated HTTP client for feature-flag and remote-config requests so `FeatureFlagRequestTimeout` is not capped by `BatchUploadTimeout`. Reject `Enqueue` with `ErrClosed` if `Close` starts while `BeforeSend` runs. Build legacy gzip batch URLs with `url.Parse` so an `Endpoint` query string is preserved.
