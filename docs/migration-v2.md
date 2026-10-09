# Migrating to posthog-go v2

v2 makes capture v1 the only capture path. The legacy `/batch/` pipeline is
removed, along with the `Config.CaptureMode` switch that chose between them.

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

Subpackages such as `posthogmcp` move with it, to `github.com/posthog/posthog-go/v2/posthogmcp`.
The MCP Go SDK adapter is a separate module that moves in lockstep with the core, so import `github.com/posthog/posthog-go/posthogmcpsdk/v2`.
The `otel` bridge versions independently and keeps its import path.

## Constructors

`New` now returns `(Client, error)`, like `NewWithConfig`. Handle the error
before using or closing the client:

```go
// before
client := posthog.New(apiKey)

// after
client, err := posthog.New(apiKey)
if err != nil {
    return err
}
defer client.Close()
```

If initialization fails, either constructor returns a nil client and an error.

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

`Client` gained `EnqueueAI`. This only affects code that *implements* the
interface — a hand-written test mock needs the new method. Callers are
unaffected.

`CompressionZstd`, `CompressionDeflate` and `CompressionBrotli` no longer
require an opt-in. A config that previously failed `Validate()` with
`"zstd compression requires CaptureModeAnalyticsV1"` now succeeds.

## Groups

`Groups` is now `map[string]string`, and `Groups.Set` takes a string value. A
group key identifies a group, so numbers, maps and other values no longer
compile. This matches `GroupIdentify.Key`, which was already a string.

Convert a numeric group ID to its decimal string:

```go
// before
groups := posthog.NewGroups().Set("company", companyID) // companyID is an int64

// after
groups := posthog.NewGroups().Set("company", strconv.FormatInt(companyID, 10))
```

v1 bucketed a numeric group key for feature flags by its decimal form, so a key
converted this way keeps every group in the same rollout bucket and variant.

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
- An event whose uuid is absent from the `results` map fires neither callback.
  A proxy or gateway that answers the capture path with its own `200` body
  therefore reports nothing at all, where the old endpoint treated any `< 300`
  as success. Confirm intermediaries pass the capture response through.
- Errors are typed. Check them with `errors.As`:

```go
func (c myCallback) Failure(msg posthog.APIMessage, err error) {
    var eventErr *posthog.CaptureEventError
    if errors.As(err, &eventErr) {
        // One event: eventErr.EventUUID, .Result ("drop"/"retry"),
        // .Details, .Exhausted when retries ran out, and .Endpoint
        // to tell the analytics and AI lanes apart.
        return
    }

    var localErr *posthog.CaptureLocalError
    if errors.As(err, &localErr) {
        // Refused before sending: localErr.Endpoint names the lane, and it
        // unwraps to ErrMessageTooBig and friends.
        return
    }

    var reqErr *posthog.CaptureRequestError
    if errors.As(err, &reqErr) {
        // Whole request: reqErr.StatusCode, .Code, .Description,
        // .Endpoint. Unwraps to the transport error when there was one.
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
travels in the `PostHog-Sdk-Info` header, which is always
`posthog-go/<version>`, and the backend sets both properties from it. If you
set them explicitly they are still sent, but the backend overwrites them, so
stored events always report `posthog-go`.

`Capture.Library` and `Exception.Library` (added in 1.33) are removed. One
request carries one header, so a per-event library name cannot reach the
backend. `posthogmcp` events now report `posthog-go`, like every other event.
Delete the field.

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
- an event option value PostHog cannot read (`Details` is `invalid_options`,
  see [Event options](#new-event-options))

A duplicate, empty or malformed event `uuid` still fails the whole batch.

## New: event options

Capture v1 reads per-event processing controls, such as person profile
processing, from an `options` object sent next to `properties`. Every message
type (`Capture`, `Identify`, `Alias`, `GroupIdentify` and `Exception`) has an
`Options` field, and it works the same with `Enqueue` and `EnqueueAI`:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "user-123",
    Event:      "signed_up",
    Options:    posthog.NewOptions().Set("process_person_profile", false),
})
```

Keys are strings and values are any JSON-serializable value. The SDK sends
options unchanged, so an option that PostHog adds later needs no SDK upgrade.
PostHog validates them: it ignores keys it does not know, reads common forms
of a known key's value (for a boolean, `"yes"`, `"off"` or `0`), and drops the
event if a known key has a value it cannot read. That drop reaches
`Callback.Failure` as a `*CaptureEventError` whose `Details` is
`invalid_options`. A `BeforeSend` hook can read and change `Options` like
`Properties`, and `Options` is never nil inside the hook.

The legacy properties still work. The SDK removes each one from `properties`
and moves it into its option:

| Legacy property | Option |
| --- | --- |
| `$process_person_profile` | `process_person_profile` |
| `$cookieless_mode` | `cookieless_mode` |
| `$ignore_sent_at` | `disable_skew_correction` |
| `$product_tour_id` | `product_tour_id` |

When both are set, the option wins. An option set to `nil` counts as not
set, so the legacy property applies.

Compared with 1.x, which sent these as properties to the `/batch/` endpoint:

- PostHog now reads common forms of the value, so
  `"$process_person_profile": "false"` turns person processing off. 1.x
  ignored that string.
- A value PostHog cannot read, such as `"$process_person_profile": "maybe"`,
  now drops the event as `invalid_options`. 1.x kept the event and ignored the
  value, except for `$cookieless_mode`, where any non-boolean value failed the
  whole batch.

If you used `CaptureModeAnalyticsV1`, note that the SDK no longer converts
these values or silently removes the ones it cannot read. PostHog validates
them as described above.

## New: AI events

`EnqueueAI` sends to PostHog's dedicated AI capture endpoint, which accepts
much larger events than the analytics endpoint:

```go
client.EnqueueAI(posthog.Capture{
    DistinctId: "user-1",
    Event:      "$ai_generation",
    Properties: props,
})
```

It runs on its own queue, batching and retry state, started on first use.
Routing is by method: `Enqueue` never reroutes an `$ai_`-prefixed event, so
existing code keeps sending those as ordinary analytics events.

Tune it with `Config.CaptureAICompression`, `Config.CaptureAIMaxQueueSize` and
`Config.CaptureAIBatchUploadTimeout` (30s, longer than the analytics lane's
because AI batches are much larger).
Use `EnqueueAIWithContext` from HTTP handlers, as you would
`EnqueueWithContext`.
