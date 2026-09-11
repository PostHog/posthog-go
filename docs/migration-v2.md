# Migrating to posthog-go v2

v2 makes capture v1 the only capture path. The legacy `/batch/` pipeline is
removed, along with the `Config.CaptureMode` switch that chose between them.

> This guide describes the changes staged on the `v1` branch.

## Import path

Go requires the major version in the module path:

```bash
go get github.com/posthog/posthog-go/v2
```

```go
// before
import "github.com/posthog/posthog-go"

// after
import "github.com/posthog/posthog-go/v2"
```

The package name is still `posthog`, so only the import line changes.

## Config

`Config.CaptureMode` is gone. If you opted into v1, delete the field — it is
now the only behavior:

```go
// before
client, err := posthog.NewWithConfig(apiKey, posthog.Config{
    Endpoint:    "https://us.i.posthog.com",
    CaptureMode: posthog.CaptureModeAnalyticsV1,
})

// after
client, err := posthog.NewWithConfig(apiKey, posthog.Config{
    Endpoint: "https://us.i.posthog.com",
})
```

The `CaptureMode` type and the `CaptureModeLegacy` / `CaptureModeAnalyticsV1`
constants are removed with it.

`CompressionZstd`, `CompressionDeflate` and `CompressionBrotli` no longer
require an opt-in. A config that previously failed `Validate()` with
`"zstd compression requires CaptureModeAnalyticsV1"` now succeeds.

## Behavior changes

None of these produce a compile error, so read them even if your code builds
unchanged.

### Endpoint and network path

Events are sent to `POST /i/v1/analytics/events` instead of `POST /batch/`.

Check anything that pins the old path: reverse proxies, API gateways, egress
allowlists, WAF rules, and self-hosted deployments. **The v1 route is only
served where the deployment enables it**, so confirm your PostHog instance
serves it before upgrading. PostHog Cloud (`us.i.posthog.com`,
`eu.i.posthog.com`) does.

### Authentication

The project API key moves from an `api_key` body field to an
`Authorization: Bearer <key>` header. Anything that inspected, logged or
rewrote the request body to find the key needs updating.

### Delivery results are per event

The old endpoint accepted or rejected a whole batch. v1 returns a result for
each event, and the client only re-sends the events the backend asks it to
retry.

Two consequences for `Callback`:

- `Failure` can fire on an HTTP **200**, for an individual event the backend
  dropped while accepting the rest of the batch.
- Errors are typed. Check them with `errors.As`:

```go
func (c myCallback) Failure(msg posthog.APIMessage, err error) {
    var eventErr *posthog.CaptureEventError
    if errors.As(err, &eventErr) {
        // One event: eventErr.EventUUID, .Result ("drop"/"retry"),
        // .Details, and .Exhausted when retries ran out.
        return
    }

    var reqErr *posthog.CaptureRequestError
    if errors.As(err, &reqErr) {
        // Whole request: reqErr.StatusCode, .Code, .Description.
        // Unwraps to the transport error when there was one.
        return
    }
}
```

### Retries

`429` is no longer retried: the capture endpoint does not emit it, and billing
limits arrive as a terminal `402`. Retryable statuses are `408`, `500`, `502`,
`503` and `504`. `Retry-After` is honoured, clamped to 30s.

### Event properties

`$lib` and `$lib_version` are no longer sent in properties. SDK identity
travels in the `PostHog-Sdk-Info` header and the backend injects the
properties from it. If you set them explicitly they are still sent.

Caller-supplied properties now take precedence over SDK system context.
Previously the SDK's `$os`, `$os_version`, `$os_distro` and `$go_version`
overwrote values you had set; they are now applied only as defaults.

### Compression

`Content-Encoding` must be one of `gzip`, `deflate`, `br` or `zstd`. The
legacy `lz64` and `base64` encodings and the `?compression=` query parameter
are gone. If a configured codec fails at runtime the batch is still sent,
uncompressed.

### Per-event validation

These are rejected individually inside an otherwise successful batch, and
surface as `*CaptureEventError`:

- event names longer than 200 bytes
- distinct IDs longer than 200 bytes
- `$performance_event`
- properties that are not a JSON object

A duplicate, empty or malformed event `uuid` still fails the whole batch.
