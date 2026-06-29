## Unreleased

## 1.17.0

### Minor Changes

- ff0e74b: Enrich capture-v1 failure reporting with typed errors and verbose result logging. When `CaptureMode` is `CaptureModeAnalyticsV1`, `Callback.Failure` now receives either a `*CaptureEventError` (a single event the server dropped, or one still asking to retry once attempts are exhausted — exposing `EventUUID`, `Result`, `Details`, and `Exhausted`) or a `*CaptureRequestError` (a whole request that failed on a non-2xx status, transport error, or malformed body — exposing `StatusCode`, `Code`, `Description`, and unwrapping to the underlying error). Inspect them with `errors.As`. Additionally, with `Verbose: true` the SDK logs one debug line per 2xx response summarizing the per-event directive counts (ok/warning/drop/retry/other) so partial-submission outcomes are easy to debug. The legacy path and its callback errors are unchanged.
- ff0e74b: Add opt-in capture-v1 support via a new `Config.CaptureMode` option. It defaults to `CaptureModeLegacy` (the existing `POST /batch/` endpoint), so upgrading is transparent and requires no code changes. Set `CaptureMode: posthog.CaptureModeAnalyticsV1` to send events to `POST /i/v1/analytics/events` instead, which uses Bearer auth, per-event delivery results, and partial retry (only the events the server asks to retry are re-sent). For v1, the `Callback` interface is unchanged but outcomes become per-event: a `drop` result fails an individual event (and can fire `Failure` on an HTTP 200), `warning` counts as success, and a uuid missing from the results is silently dropped. All other configuration (host, API key, `Callback`, `Logger`, `MaxRetries`, `Compression`) is reused across both modes.
- ff0e74b: Add zstd, deflate, and brotli compression for the capture-v1 path, alongside the existing gzip. Select one via `Config.Compression` (`CompressionZstd`, `CompressionDeflate`, `CompressionBrotli`) and the SDK sets the matching `Content-Encoding` header (`zstd`, `deflate`, `br`). These three codecs require `CaptureMode: posthog.CaptureModeAnalyticsV1` — the legacy `POST /batch/` endpoint only understands gzip, so configuring them with `CaptureModeLegacy` returns a validation error. `CompressionGzip` and `CompressionNone` continue to work on both capture modes. All codecs use pure-Go libraries (stdlib `compress/zlib`, `github.com/klauspost/compress/zstd`, `github.com/andybalholm/brotli`), so `CGO_ENABLED=0` builds are unaffected.

## 1.16.2

### Patch Changes

- 76de226: Dedupe `$feature_flag_called` events by feature flag response value.

## 1.16.1

### Patch Changes

- 922cfff: Validate user-supplied event UUIDs before sending and generate a fallback UUID when invalid.

## 1.16.0

### Minor Changes

- 1068ec9: Add a BeforeSend hook for modifying or dropping messages before they are sent.

## 1.15.1

### Patch Changes

- 52b7373: Stop sending ignored top-level capture fields and rely on canonical event properties.

## 1.15.0

### Minor Changes

- 64ad172: Support the `early_exit` flag filter in local evaluation. When a flag's `filters.early_exit` is `true` and a condition group's property filters match (or there are none) but the rollout percentage excludes the user, evaluation now stops and returns `false` immediately instead of falling through to later groups. Mirrors the server-side (Rust) evaluation engine. A property-filter mismatch still falls through as before, and behaviour is unchanged when `early_exit` is unset or `false`.

### Minor Changes

- Support the `early_exit` option on feature flag filters during local evaluation. When a flag has `filters.early_exit` set to `true` and a condition group matches its property filters (or has none) but the rollout percentage excludes the user, local evaluation now returns a definitive disabled result immediately instead of falling through to later condition groups, mirroring the server-side evaluation engine. Property-filter mismatches continue to fall through as before, and behaviour is unchanged when `early_exit` is absent or `false`.

## 1.14.0

### Minor Changes

- 554c99a: Add a configurable `$is_server` event property (default `true`) so PostHog can identify server-side events. Set `IsServer: false` when using posthog-go as a client/CLI so the device OS is attributed normally.

## 1.13.2

### Patch Changes

- df2ae97: Capture with `SendFeatureFlags(true)` now prefers local evaluation when flag definitions are loaded, falling back to a remote `/flags` request only for flags that can't be computed locally. `OnlyEvaluateLocally` remains strictly local with no remote fallback.

  Captured events now exclude flags that evaluate to `false` from `$active_feature_flags` (they are still attached as `$feature/<key>=false`), matching the other PostHog SDKs.

## 1.13.1

### Patch Changes

- 541b82f: Include group context in the `$feature_flag_called` LRU dedupe key so group-scoped flags fire a separate event for each group a user is evaluated under, instead of being dedup-ed against the first group context the same `(distinct_id, flag, device_id)` was seen under.

## 1.13.0

### Minor Changes

- dec8ade: Add opt-in panic capture for request context middleware.
- dec8ade: Add server-side request context helpers for net/http capture and exception events, plus `EvaluateFlagsWithContext` for using request-scoped distinct IDs during flag evaluation. Request-context flag evaluation does not generate personless IDs.

## 1.12.6

### Patch Changes

- 9289d53: Reject semver values with leading zeros in local flag evaluation. Per semver 2.0.0 §2, numeric identifiers must not include leading zeros — values like `1.07.3` are not valid semver and should not match targeting conditions. Both override values and flag values are now validated; invalid inputs surface an `InconclusiveMatchError` so the condition does not match.

## 1.12.5

### Patch Changes

- 6d243a6: Return ErrSDKDisabled from no-op clients when the project API key is missing, return ErrNoPersonalAPIKey before making requests for Personal API key dependent methods when no Personal API key is configured, and return ErrNoDistinctID from EvaluateFlags when distinct_id is missing.

### New Features

- **`EvaluateFlags`**: New method on `Client` that returns a `FeatureFlagEvaluations` snapshot for a user using a single `/flags` request. The snapshot powers any number of `IsEnabled` / `GetFlag` / `GetFlagPayload` checks, fires deduped `$feature_flag_called` events with full v4 metadata (id, version, reason, request_id), and can be attached to a `Capture` event via the new `Capture.Flags` field to populate `$feature/<key>` and `$active_feature_flags` without another network call.
- **`Capture.Flags`**: New optional field on `Capture` that accepts a `*FeatureFlagEvaluations` snapshot. Takes precedence over `SendFeatureFlags`, avoids a hidden `/flags` request per event, and lets caller-supplied `Properties` override the auto-generated `$feature/<key>` values on conflict.

### Internal

- Refactored the `$feature_flag_called` dedup logic into a shared helper so the existing single-flag path and the new snapshot path use identical semantics against the same per-distinct_id LRU cache.
- `$feature_flag_called` events from the snapshot path combine response-level errors (`errors_while_computing_flags`, `quota_limited`) with per-flag errors (`flag_missing`) comma-joined in `$feature_flag_error`, matching the granularity of the legacy single-flag path.

## 1.12.4 - 2026-04-30

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.12.3...v1.12.4)

## 1.12.3 - 2026-04-21

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/1.12.2...1.12.3)

## 1.12.2 - 2026-04-20

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/1.12.1...1.12.2)

## 1.12.1 - 2026-04-20

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.12.0...v1.12.1)

## 1.12.0 - 2026-04-20

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.11.3...v1.12.0)

## 1.11.3 - 2026-04-14

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.11.2...v1.11.3)

- Added `locally_evaluated` property to `$feature_flag_called` events, indicating whether the flag was evaluated locally or via the remote `/flags` endpoint.

## 1.11.2 - 2026-03-26

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.11.1...v1.11.2)

## 1.11.1 - 2026-03-11

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.11.0...v1.11.1)

## 1.10.0 - 2026-02-04

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.9.1...v1.10.0)

### New Features

- **`GetFeatureFlagResult`**: New method that returns both the flag value and payload in a single call, while properly tracking feature flag usage via `$feature_flag_called` events.

### Deprecations

- **`GetFeatureFlagPayload`**: Deprecated in favor of `GetFeatureFlagResult`. The new method provides better tracking and a more convenient API.

### Migration Guide

```go
// Before (two calls, no event tracking for payload-only):
flag, _ := client.GetFeatureFlag(payload)
payloadStr, _ := client.GetFeatureFlagPayload(payload)

// After (single call, always tracks):
result, err := client.GetFeatureFlagResult(payload)
if err != nil { /* handle */ }
if result.Enabled {
    var config MyConfig
    result.GetPayloadAs(&config)
}
```

**Note**: `GetFeatureFlagResult` returns `nil, error` when a flag doesn't exist (rather than a result with `Enabled: false`). Check for errors to distinguish between a disabled flag and a missing flag:

```go
result, err := client.GetFeatureFlagResult(payload)
if errors.Is(err, posthog.ErrFlagNotFound) {
    // Flag doesn't exist - use default behavior
}
if err != nil {
    // Other error (e.g., network issue)
}
if result.Enabled {
    // Flag exists and is enabled
} else {
    // Flag exists but is disabled
}
```

## 1.9.1 - 2026-01-21

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.9.0...v1.9.1)

## 1.9.0 - 2026-01-13

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.8.2...v1.9.0)

## 1.8.2

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.8.1...v1.8.2)

## 1.8.1

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.8.0...v1.8.1)

## 1.8.0

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.7.0...v1.8.0)

# 1.7.0

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.13...v1.7.0)

## 1.6.13

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.12...v1.6.13)

## 1.6.12

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.11...v1.6.12)

## 1.6.11

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.10...v1.6.11)

## 1.6.10

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.9...v1.6.10)

## 1.6.9

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.8...v1.6.9)

## 1.6.8

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.7...v1.6.8)

## 1.6.7

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.6...v1.6.7)

## 1.6.6

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.5...v1.6.6)

## 1.6.5

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.4...v1.6.5)

## 1.6.4

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.3...v1.6.4)

## 1.6.3

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.2...v1.6.3)

## 1.6.2

- Fix: Pass project API key in remote_config requests
- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.1...v1.6.2)

## 1.6.1

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.6.0...v1.6.1)

## 1.5.15

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.14...v1.5.15)

## 1.5.14

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.13...v1.5.14)

## 1.5.13

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.12...v1.5.13)

## 1.5.12

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.11...v1.5.12)

## 1.5.11

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.10...v1.5.11)

## 1.5.10

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.9...v1.5.10)

## 1.5.9

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.8...v1.5.9)

## 1.5.8

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.7...v1.5.8)

## 1.5.7

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.6...v1.5.7)

## 1.5.6

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.5...v1.5.6)

## 1.5.5

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.4...v1.5.5)

## 1.5.4

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.3...v1.5.4)

## 1.5.3

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.2...v1.5.3)

## 1.5.2

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.1...v1.5.2)

## 1.5.1

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.5.0...v1.5.1)

## 1.4.10

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.9...v1.4.10)

## 1.4.9

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.8...v1.4.9)

## 1.4.8

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.7...v1.4.8)

## 1.4.7

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.6...v1.4.7)

## 1.4.6

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.5...v1.4.6)

## 1.4.5

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.4...v1.4.5)

## 1.4.4

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.3...v1.4.4)

## 1.4.3

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.2...v1.4.3)

## 1.4.2

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.1...v1.4.2)

## 1.4.1

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.4.0...v1.4.1)

## 1.4.0

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.3.3...v1.4.0)

## 1.3.3

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.3.2...v1.3.3)

## 1.3.2

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.3.1...v1.3.2)

## 1.3.1

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.3.0...v1.3.1)

## 1.2.24

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.23...v1.2.24)

## 1.2.23

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.22...v1.2.23)

## 1.2.22

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.21...v1.2.22)

## 1.2.21

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.20...v1.2.21)

## 1.2.20

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.19...v1.2.20)

## 1.2.19

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.18...v1.2.19)

## 1.2.18

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.17...v1.2.18)

## 1.2.17

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.16...v1.2.17)

## 1.2.16

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.15...v1.2.16)

## 1.2.15

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.14...v1.2.15)

## 1.2.14

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.13...v1.2.14)

## 1.2.13

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v1.2.12...v1.2.13)

## 1.2.12

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.12)

## 1.2.11

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.11)

## 1.2.10

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.10)

## 1.2.9

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.9)

## 1.2.8

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.8)

## 1.2.7

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.7)

## 1.2.6

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.6)

## 1.2.5

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.5)

## 1.2.4

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.4)

## 1.2.3

- [Full Changelog](https://github.com/PostHog/posthog-go/compare/v...v1.2.3)

# Changelog

## 1.2.2 - 2024-08-08

1. Adds logging to error responses from the PostHog API so that users can see how a call failed (e.g. rate limiting) from the SDK itself.
2. Better string formatting in `poller.Errorf`

## 1.2.1 - 2024-08-07

1. The client will fall back to the `/decide` endpoint when evaluating feature flags if the user does not wish to provide a PersonalApiKey. This fixes an issue where users were unable to use this SDK without providing a PersonalApiKey. This fallback will make feature flag usage less performant, but will save users money by not making them pay for public API access.

## 1.2.0 - 2022-08-15

Breaking changes:

1. Minimum PostHog version requirement: 1.38
2. Local Evaluation added to IsFeatureEnabled and GetFeatureFlag. These functions now accept person and group properties arguments. The arguments will be used to locally evaluate relevant feature flags.
3. Feature flag functions take a payload called `FeatureFlagPayload` when a key is require and `FeatureFlagPayloadNoKey` when a key is not required. The payload will handle defaults for all unspecified arguments automatically.
4. Feature Flag defaults have been removed. If the flag fails for any reason, nil will be returned.
5. GetAllFlags argument added. This function returns all flags related to the id.
