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

`Config.MaxEventBytes` and `Config.MaxBatchBytes` are new. Both default to
500000 bytes, the limits 1.x applied internally. An event over
`MaxEventBytes` is refused locally with `ErrMessageTooBig`, and
`MaxBatchBytes` bounds the size of one request. `Validate`, and so
`NewWithConfig`, rejects a config where `MaxBatchBytes` is less than
`MaxEventBytes`. Neither applies to `EnqueueAI` (see
[AI events](#new-ai-events)).

`Config.MaxRetryBackoff` is new (see [Retries](#retries)).

`Validate` now rejects a negative `BatchUploadTimeout`, `MaxRetryBackoff`,
`MaxEventBytes` or `MaxBatchBytes`. Only zero selects the default.

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

Three consequences for `Callback`:

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

Local failures, such as an event over `MaxEventBytes` or a full queue, reach
`Failure` wrapped in a `*CaptureLocalError`. In 1.x they were the bare
sentinel errors, so `err == posthog.ErrMessageTooBig` no longer matches. Use
`errors.Is(err, posthog.ErrMessageTooBig)`, which works in both. The new
`Endpoint` field on all three error types names the lane: the analytics path
or the AI path.

`Failure` can run on the goroutine that called `Enqueue`: a message that fails
JSON serialization is reported before `Enqueue` returns. Do not re-enqueue the
same message from `Failure`, because it fails again and the calls recurse.
This was also true in 1.x.

Without a `Callback`, the SDK logs one warning per batch that loses events,
for example `analytics: 3 event(s) dropped: <cause>; set Config.Callback to
inspect failures`. For a failed request the cause is the error text, which
can include PostHog's error description, as 1.x logged the response body.

### Retries

`429` is no longer retried: the capture endpoint does not emit it, though a
proxy in front of it can, and billing limits arrive as a terminal `402`.
Retryable statuses are `408`, `500`, `502`, `503` and `504`, plus network
errors. `Retry-After` is honoured, clamped to the new `Config.MaxRetryBackoff`
(default 30s), which also caps the default exponential backoff. A custom
`Config.RetryAfter` is used as given.

### Event IDs

The SDK generates UUIDv7 values for event `Uuid`s, the `PostHog-Request-Id`
header and the distinct ID of a personless capture. It falls back to v4 if v7
generation fails. A `Uuid` you set is normalized to the lowercase hyphenated
form, so the `Callback` message carries that form. An empty or invalid `Uuid`
is replaced with a generated one before `BeforeSend`.

### Session and window IDs

The SDK sends `$session_id` and `$window_id` as top-level fields of the event,
because capture reads them there and requires strings. A value whose JSON form
is a string is sent as that string: a `string`, including `""`, a named string
type, or a `uuid.UUID`. Any other value is dropped, and the SDK logs a warning
for each drop that names the key and the value's type, never the value. A `nil`
value counts as unset and drops without a warning. In 1.x the SDK sent any
value as a property.

### Event properties

`$lib` and `$lib_version` are no longer sent in properties. SDK identity
travels in the `PostHog-Sdk-Info` header, which is always
`posthog-go/<version>`, and the backend sets both properties from it. If you
set them explicitly, the SDK removes them before it sends the event, so stored
events always report `posthog-go`. `BeforeSend` still sees the value you set.

`Capture.Library` and `Exception.Library` (added in 1.33) are removed. One
request carries one header, so a per-event library name cannot reach the
backend. `posthogmcp` events now report `posthog-go`, like every other event.
Delete the field.

`$lib_version` now reports the SDK version from a Go test binary too. 1.x
reported `1.0.0` from any test binary, including your application's own.

`Config.DefaultEventProperties` are now defaults too. The SDK fills them in
before `BeforeSend`, only for keys that the event and the request context left
unset. A key set to `nil` counts as set, so it keeps the default out.
`RequestContext.Properties` fill in the same way and win over the defaults. In
1.x the default value overwrote the event's value, except for an event's
`$process_person_profile: false`.

`$set`, `$set_once`, `$groups` and `$group_set` fill one level deep. When the
event and a default both set one of them to a map, the default adds only the
keys the event left out. For example, a default
`"$set": {"plan": "free", "source": "web"}` and an event
`"$set": {"plan": "pro"}` send `{"plan": "pro", "source": "web"}`. In 1.x the
default replaced the event's whole map. `Capture.Groups` now merges into a
`$groups` map in `Properties` and wins key by key; in 1.x it replaced it.

`BeforeSend` sees the filled `DefaultEventProperties`,
`RequestContext.Properties` and SDK values (below), and can change or remove
them. Its changes are
final. The request context's session ID is part of the event's identity, so it
is set as `$session_id` first, unless the event has its own.

`DefaultEventProperties` now apply to `Exception`, `Identify`, `Alias` and
`GroupIdentify` events too, not only to `Capture`. For `Identify`, `Alias` and
`GroupIdentify` they fill the new `EventProperties` field: the properties of
the event itself, separate from the `$set` or `$group_set` that `Properties`
sends. A `$set` in `Identify.EventProperties` or a `$group_set` in
`GroupIdentify.EventProperties` merges under `Properties`, which win key by
key. The `Config.Callback` message for an `Alias` does not include its
`EventProperties`.

The values the SDK adds are defaults too. The SDK fills them before
`BeforeSend`, only for keys the caller left unset:

- `$is_server`, from `Config.IsServer`
- `$geoip_disable`, from `Config.DisableGeoIP`
- `$os`, `$os_version`, `$os_distro` and `$go_version`
- `$feature/<key>` and `$active_feature_flags`, from `SendFeatureFlags`

The first three fill last, under the event, the request context and
`DefaultEventProperties`. The feature flag properties fill right under the
event's own `Properties`, like the `Flags` snapshot, so they win over the
request context and `DefaultEventProperties`.

In 1.x the SDK overwrote the values you set for these. So a
`DefaultEventProperties` value of `$geoip_disable: false` now turns GeoIP
lookup back on for events, even when `Config.DisableGeoIP` is on. Feature flag
requests still follow `Config.DisableGeoIP`. The `IsServer` and `DisableGeoIP`
fields of a message report the values left after `BeforeSend`. To change
them, set the properties.

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

Each event needs its own uuid. Capture rejects a whole request that contains
the same uuid twice, and the SDK reports it as a `*CaptureRequestError` (400,
`duplicate_event_uuid`). A malformed `Uuid` set in `BeforeSend` also fails
the whole request.

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

Options can also be set for every event and for a request:

- `Config.DefaultEventOptions` applies to every `Capture`, `Exception`,
  `Identify`, `Alias` and `GroupIdentify` event, like
  `Config.DefaultEventProperties`.
- `RequestContext.Options` applies to the `Capture` and `Exception` events
  sent with `EnqueueWithContext` or `EnqueueAIWithContext`, like
  `RequestContext.Properties`. A child context inherits its parent's options.

The event's `Options` come first. Before `BeforeSend`, the SDK fills each
option that is still missing or `nil`, from these layers in order:
`RequestContext.Options`, then `DefaultEventOptions`, then the SDK's
personless default (below). So a `nil` event option still gets the default.
`BeforeSend` sees the filled options and can change or remove them. To turn a
default off for one event, set the option to `false` on the event or in
`BeforeSend`. To change an option in `BeforeSend`, set it in `Options`: a
legacy property that the hook adds does not replace an option that is already
set.

An option at any layer wins over the matching legacy property at any layer.
For example, `DefaultEventOptions` with `cookieless_mode: true` wins over an
event's `$cookieless_mode: false`, and the SDK still removes
`$cookieless_mode` from the properties. When you move a default to options,
move the per-event overrides of that key to options too.

When a `Capture` or `Exception` with a request context has no distinct ID, the
SDK generates
one and sets the option `process_person_profile: false`, so it does not create
a person for every generated ID. A default, request-context or event option
can turn processing back on; a legacy `$process_person_profile` property
cannot. `posthogmcp` sets the same option on events without an identity, and
on events whose properties set `$process_person_profile: false`. This covers
tool calls and the `$mcp_unknown_tool`, `$mcp_input_required` and
`$mcp_missing_capability` events.

`posthogmcp.ToolCall` has a new `Options` field, and tool calls also take
`RequestContext.Options`. The call's options override the request context's.
The other MCP events take the same layers through `EventContext.Options`.
A call without an identity always gets `process_person_profile: false`, whatever
its options say. With an identity, a `process_person_profile` option overrides
a `$process_person_profile: false` property.

`posthogmcp` events take `RequestContext.Properties` under the call's
properties with any client, as in 1.x, and `RequestContext.Options` under the
call's options in the same way. The call's values win, and the context's values win over
`DefaultEventProperties` and `DefaultEventOptions`. `posthogmcp` still drops
the reserved keys, such as `$mcp_*`, `$set` and `$session_id`, from the
context properties and sanitizes their values first.

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

Minimal `$feature_flag_called` events now keep `$cookieless_mode`,
`$ignore_sent_at` and `$product_tour_id`, so they get those options. 1.x
removed them from these events.

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
existing code keeps sending those as ordinary analytics events. `EnqueueAI`
accepts every message type and does no routing check of its own: capture
decides, and returns a per-event drop for an event the AI endpoint does not
take. `Flush` and `FlushWithContext` also wait for the AI lane once
`EnqueueAI` has started it.

The AI lane's size limits are fixed, because they track the endpoint: an event
over 8 MiB plus 64 KiB of envelope headroom is refused locally, and a batch
closes at about 5 MiB. `MaxEventBytes` and `MaxBatchBytes` do not apply to it.

Tune it with `Config.CaptureAICompression`, `Config.CaptureAIMaxQueueSize` and
`Config.CaptureAIBatchUploadTimeout` (30s, longer than the analytics lane's
because AI batches are much larger).
Use `EnqueueAIWithContext` from HTTP handlers, as you would
`EnqueueWithContext`. It takes a `Client` rather than an `EnqueueClient`,
because only a real client has an AI lane.
