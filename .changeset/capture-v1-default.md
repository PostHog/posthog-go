---
"posthog-go": major
---

Capture v1 is now the only capture path. The legacy `/batch/` pipeline and the `Config.CaptureMode` switch that selected between them are removed.

See [the migration guide](docs/migration-v2.md) for the full details.

### Breaking changes

- `Config.CaptureMode`, the `CaptureMode` type, and `CaptureModeLegacy`/`CaptureModeAnalyticsV1` are removed. Delete the field; there is nothing to opt into.
- `CompressionZstd`, `CompressionDeflate` and `CompressionBrotli` are now valid unconditionally. Configurations that previously failed validation with "requires CaptureModeAnalyticsV1" now succeed.

### Behavior changes

These apply to every caller on upgrade, and none of them produce a compile error.

- **Events go to `POST /i/v1/analytics/events` instead of `POST /batch/`.** Reverse proxies, egress allowlists and self-hosted deployments pinned to `/batch/` must be updated. The v1 route is only served where the deployment enables it.
- **Authentication moves from the request body to `Authorization: Bearer`.** The project API key is no longer sent as an `api_key` body field.
- **Delivery results are per event, not per batch.** Only the events the backend asks to retry are re-sent, and `Callback.Failure` can now fire on an HTTP 200 for an individual event that was dropped. Failures carry typed `*CaptureEventError`, `*CaptureRequestError` and `*CaptureLocalError` values.
- **429 is no longer retried.** The capture endpoint does not emit it; billing limits arrive as a terminal 402. `Retry-After` is honoured but clamped to `Config.MaxRetryBackoff` (default 30s).
- **`$lib` and `$lib_version` are no longer sent in event properties.** SDK identity travels in the `PostHog-Sdk-Info` header and the backend injects the properties. A `$lib` or `$lib_version` you set is removed before the event is sent.
- **Values you set now win over the values the SDK adds.** `$is_server`, `$geoip_disable`, `$os`, `$os_version`, `$os_distro` and `$go_version` fill only keys the event, the request context and `DefaultEventProperties` left unset, before `BeforeSend`. The `$feature/<key>` and `$active_feature_flags` properties from `SendFeatureFlags` fill only keys the event's own properties left unset, so they win over the request context and `DefaultEventProperties`. Previously the SDK overwrote them. A `$geoip_disable: false` default now turns GeoIP lookup back on for events, even with `Config.DisableGeoIP` on.
- **Compression is stricter.** `gzip`, `deflate`, `br` and `zstd` are accepted via `Content-Encoding`; the legacy `lz64`/`base64` encodings and the `?compression=` query parameter are gone.
- **Some invalid events are now rejected individually** rather than failing the batch: event names and distinct IDs over 200 bytes, and `$performance_event`.
