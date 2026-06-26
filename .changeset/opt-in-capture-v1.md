---
"posthog-go": minor
---

Add opt-in capture-v1 support via a new `Config.CaptureMode` option. It defaults to `CaptureModeLegacy` (the existing `POST /batch/` endpoint), so upgrading is transparent and requires no code changes. Set `CaptureMode: posthog.CaptureModeAnalyticsV1` to send events to `POST /i/v1/analytics/events` instead, which uses Bearer auth, per-event delivery results, and partial retry (only the events the server asks to retry are re-sent). For v1, the `Callback` interface is unchanged but outcomes become per-event: a `drop` result fails an individual event (and can fire `Failure` on an HTTP 200), `warning` counts as success, and a uuid missing from the results is silently dropped. All other configuration (host, API key, `Callback`, `Logger`, `MaxRetries`, `Compression`) is reused across both modes.
