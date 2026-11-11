---
title: "PostHog Go SDK migration guides"
sidebar: Docs
showTitle: true
---

# Migrating to posthog-go v2

v2 makes capture v1 the only capture path. The legacy `/batch/` pipeline is
removed, along with the `Config.CaptureMode` switch that chose between them.

For the changes every capture v1 SDK shares, such as event options, limits
and per-event results, see
[Upgrading to capture v1 SDKs](https://posthog.com/docs/libraries/capture-v1).

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
The mcp-go adapter is one too: import `github.com/posthog/posthog-go/posthogmcpgo/v2`.
The `otel` bridge imports the core SDK, so it also moves in lockstep with the core: import `github.com/posthog/posthog-go/otel/v2`.

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

Either constructor returns a nil client and an error only for invalid
configuration. An empty project API key still gives a disabled client and a
nil error, as in 1.x, and a personal (`phx_...`) key now does too. Both log an
error. A disabled client
sends nothing, and its methods return default values and `ErrSDKDisabled`.
Its text is now `posthog SDK is disabled`, so code that compares error strings
should use `errors.Is(err, posthog.ErrSDKDisabled)` instead.
Set `Config.Disabled` to build one on purpose, for example in tests.

Integrations follow the same rule. `posthogmcpsdk.NewMiddleware` and
`Instrument` no longer panic on a nil `*posthogmcp.Analytics`: they report one
error and install middleware that changes nothing. The `otel` bridge's
`NewExporter` and `NewSpanProcessor` no longer return an error for an empty or
personal API key: they log one error and return an exporter or processor that
exports nothing. They still return an error for an invalid `WithHost`.

## Config

`Config.CaptureMode` is gone. If you opted into v1, delete the field. It is
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
interface: a hand-written test mock needs the new method. Callers are
unaffected.

`Alias` gained the `EventProperties` and `Options` maps, and `Backoff` gained
a function field. Neither type can be compared with `==` or used as a map key
any more, so code that does this no longer compiles.

`CompressionZstd`, `CompressionDeflate` and `CompressionBrotli` no longer
require an opt-in. A config that previously failed `Validate()` with
`"zstd compression requires CaptureModeAnalyticsV1"` now succeeds.

`Config.MaxBatchBytes` is new. It defaults to 500000 bytes, the request limit
1.x applied internally. It targets the combined serialized size of the events
in one request, excluding the request envelope. An event larger than
`MaxBatchBytes` is sent in a request of its own.

There is no per-event size cap by default, unlike the 500000-byte cap in 1.x.
Within PostHog's request limits, PostHog now drops an oversized event on its
own and reports it to `Callback`, without failing the other events in the
request. Set the new
`Config.MaxEventBytes` to refuse large events locally with `ErrMessageTooBig`;
`Validate`, and so `NewWithConfig`, then rejects a `MaxBatchBytes` below it.
Neither applies to `EnqueueAI` (see [AI events](#new-ai-events)).

`Config.MaxRetryBackoff` is new (see [Retries](#retries)).

`Validate` now rejects a negative `BatchUploadTimeout`,
`CaptureAIBatchUploadTimeout`, `MaxRetryBackoff`, `MaxEventBytes`,
`MaxBatchBytes` or `CaptureAIMaxQueueSize`. Only zero selects the default.

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

1.x bucketed a numeric group key for feature flags by its decimal form, so a key
converted this way keeps every group in the same rollout bucket and variant.

## Behavior changes

None of these produce a compile error, so read them even if your code builds
unchanged.

### Endpoint and network path

Events are sent to `POST /i/v1/analytics/events` instead of `POST /batch/`.

Check anything that pins the old path: reverse proxies, API gateways, egress
allowlists, WAF rules, and self-hosted deployments. **The v1 routes are only
served where the deployment enables them**, so confirm your PostHog instance
serves both `/i/v1/analytics/events` and `/i/v1/ai/events` before upgrading.
On self-hosted PostHog, a separate capture service serves the AI route, so
check it too if you use `EnqueueAI`. PostHog Cloud (`us.i.posthog.com`,
`eu.i.posthog.com`) serves both.

If you send events through your own reverse proxy, update it before you
upgrade:

- Forward `/i/v1/analytics/events` and `/i/v1/ai/events` to PostHog.
- Pass `Authorization` (the project API key, sent as `Bearer <key>`), every
  `PostHog-*` header, `Content-Type`, `Content-Encoding` and `User-Agent`
  through unchanged. PostHog answers `401` without `Authorization` and `400`
  without the others, and the batch fails. If your proxy strips credentials,
  keep an `Authorization` header that starts with `Bearer phc_`,
  `Bearer phs_` or `Bearer phx_`, because PostHog SDKs send their keys there.
  Keys created before these prefixes need their exact `Bearer <key>` value
  allowed too.
- Accept request bodies up to 20 MB. AI requests are often larger than 1 MB,
  the default limit in nginx and ingress-nginx. A `413` from the proxy fails
  the batch.

PostHog's managed reverse proxy already does this. See the
[proxy guides](https://posthog.com/docs/advanced/proxy).

### Redirects

A capture request follows only `307` and `308` redirects, and only to the same
scheme, host and port as `Endpoint`, at most 5 times. It resends the same body
and headers. Any other redirect fails the batch without a retry:
`Callback.Failure` gets a `CaptureRequestError` with the redirect's status.
1.x followed up to 10 redirects of any kind to any host. A `301`, `302` or
`303` resent the request as a `GET` without the events, and a `307` or `308`
to another host received the events and the API key. Feature flag requests are
unchanged.

### Request context

`RequestContext.Properties` and `RequestContext.Options` now apply to
`Identify`, `Alias` and `GroupIdentify` events sent with `EnqueueWithContext`,
not only to `Capture` and `Exception`. The context's session ID fills
`Identify` and `Alias`, but never `GroupIdentify`. A session's person in
PostHog follows the distinct IDs of the session's events, so an event sent
under a different distinct ID with the context's session ID can change the
person shown for that session. The context's distinct ID never changes an
`Identify` or an `Alias`.

An empty or whitespace-only `DistinctId` is now treated as missing, so a
`Capture` or `Exception` sent with a request context gets the context's ID or a
personless one. In 1.x it was sent, and PostHog dropped the event.

Without a request context, `Enqueue` now returns a `FieldError` for a
`Capture` or `Exception` whose `DistinctId` is only whitespace. It does the
same, with or without a request context, for an `Identify` or `Alias` whose
`DistinctId` is only whitespace and for an `Alias` whose `Alias` is only
whitespace. 1.x sent these events.

PostHog drops an event whose distinct ID is over 200 UTF-8 bytes, with the
per-event result `distinct_id_too_large`, which reaches `Callback.Failure` as a
`CaptureEventError`. 1.x sent events to legacy capture, which shortened the ID
to 200 characters and kept the event.

### Group identify distinct ID

`GroupIdentify.DistinctId` is now sent as the event's distinct ID. If it is
empty, the SDK uses the request context's distinct ID, then `$<type>_<key>`.
In 1.x the field was accepted and ignored, and every `$groupidentify` was sent
as `$<type>_<key>`.
If `$<type>_<key>` is over 200 bytes, set `DistinctId`, or PostHog drops the
event.

### Authentication

The project API key moves from an `api_key` body field to an
`Authorization: Bearer <key>` header. Anything that inspected, logged or
rewrote the request body to find the key needs updating.

### Delivery results are per event

The old endpoint accepted or rejected a whole batch. v1 returns a result for
each event, and the client only re-sends the events the backend asks it to
retry.

What this means for `Callback`:

- `Failure` can fire on an HTTP **200**, for an individual event the backend
  dropped while accepting the rest of the batch.
- A `2xx` whose body is not JSON, or whose `results` is missing, `null` or
  not an object, fails the whole batch: each event reaches `Failure` with a
  `*CaptureRequestError` and is not resent. The old endpoint treated any
  `< 300` as success, so a proxy or gateway that answers the capture path with
  its own `200` body now shows up as failures. Confirm intermediaries pass the
  capture response through.
- An event whose uuid is absent from a valid `results` object fires neither
  callback.
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

Local failures, such as an event over `MaxEventBytes` or a full
batch-submission queue, reach `Failure` wrapped in a `*CaptureLocalError`. A
full message queue is different: `Enqueue` returns `ErrQueueFull` and
`Failure` is not called, so check the error `Enqueue` returns. In 1.x they were the bare
sentinel errors, so `err == posthog.ErrMessageTooBig` no longer matches. Use
`errors.Is(err, posthog.ErrMessageTooBig)`, which works in both. The new
`Endpoint` field on all three error types names the lane: the analytics path
or the AI path.

`Failure` can run on the goroutine that called `Enqueue`: a message that fails
JSON serialization is reported before `Enqueue` returns. Do not re-enqueue the
same message from `Failure`, because it fails again and the calls recurse.
This was also true in 1.x.

Without a `Callback`, the SDK logs one warning each time a request loses
events, never one per event, for example `analytics: 3 event(s) dropped:
<cause>; set Config.Callback to inspect failures`. For a failed request the
cause is the error text, which can include PostHog's error description, as
1.x logged the response body. Events that still fail after the last retry are
logged as an error instead, with or without a `Callback`, for example
`analytics: 3 messages dropped after 4 attempts`.

Capture log lines now start with the lane name, `analytics:` or
`capture-ai:`, and some of their text changed. Update log-based alerts that
match the 1.x text. With `Verbose` on, 1.x logged every queued message in
full. v2 logs batch counts and sizes, not the messages.

### Retries

Retryable statuses are `404`, `408`, `429`, `500`, `502`, `503` and `504`,
plus network errors. 1.x did not retry `404`, and retried every `5xx`; other
`5xx` statuses are now terminal. A `404` that lasts through every attempt can
mean the PostHog deployment has no capture v1 route, for example an older
self-hosted install. Billing limits arrive as a terminal `402`. `Retry-After`
is honoured, clamped to the new `Config.MaxRetryBackoff`
(default 30s), which also caps the default exponential backoff. A custom
`Config.RetryAfter` is used as given.

Default retry waits now vary randomly by up to 25% either way, so clients
that fail together do not all retry at the same moment. `DefaultBackoff()`
now has the same jitter; in 1.x it had none. A server `Retry-After` stays the
minimum wait. A custom `Config.RetryAfter` is used as given.

### OpenTelemetry exception trace IDs

`posthogotel.EnqueueWithContext` adds the active span's `$trace_id` and
`$span_id` only when neither the event nor the request context sets either
key. In 1.37 each ID filled on its own, so an event that set only `$trace_id`
got the active span's `$span_id`.
The helper cannot see `Config.DefaultEventProperties`, so a `$trace_id` or
`$span_id` set there does not stop the derived pair, which wins over it.

### Event IDs

The SDK generates UUIDv7 values for event `Uuid`s, the `PostHog-Request-Id`
header and the distinct ID of a personless capture. It falls back to v4 if v7
generation fails. A `Uuid` you set is normalized to the lowercase hyphenated
form, so the `Callback` message carries that form. An empty `Uuid` gets a
generated one. An invalid `Uuid` is replaced with a generated one, and the SDK
logs one warning that does not include the value. The SDK applies this rule
before `BeforeSend` and again to a `Uuid` that `BeforeSend` sets.

### Timestamps

The event `timestamp`, the batch `created_at` and the
`PostHog-Request-Timestamp` header are sent in UTC with at least millisecond
digits, for example `2025-01-02T03:04:05.000Z`. The SDK keeps finer precision
from your `Timestamp` or the clock, up to nanoseconds. 1.x dropped trailing
zeros from event timestamps, so a timestamp on a whole second had no fraction.

### Session and window IDs

The SDK sends `$session_id` and `$window_id` as top-level fields of the event,
because capture reads them there and requires strings. A value whose JSON form
is a string is sent as that string: a `string`, including `""`, a named string
type, or a `uuid.UUID`. Any other value is dropped, and the SDK logs a warning
for each drop that names the key and the value's type, never the value. A `nil`
value counts as unset and drops without a warning. In 1.x the SDK sent any
value as a property.

### Event properties

Every event carries `$lib` and `$lib_version` in its properties. By default
they are `posthog-go` and the SDK version. PostHog stores the values the event
is sent with.

You can set them in the event, the request context or
`DefaultEventProperties`. Like any other property, each one comes from the most
specific place that sets it, so set both in the same place. If no `$lib` is set
to a non-empty string, the SDK sets both and replaces a `$lib_version` set on
its own. If `$lib` is `posthog-go` with no version, the SDK adds its version.
It doesn't add its version to any other `$lib`. `BeforeSend` sees both and can
change or remove them.

If an event has no `$lib`, PostHog sets both from the `PostHog-Sdk-Info`
header, `posthog-go/<version>`. In 1.x the default `/batch/` path overwrote a
`$lib` you set.

`Capture.Library` and `Exception.Library` (added in 1.30.0) are removed. Set
`$lib` in the event's properties instead. For example,
`Capture{..., Library: "my-lib"}` becomes
`Capture{..., Properties: posthog.NewProperties().Set("$lib", "my-lib").Set("$lib_version", "1.2.3")}`.
In 1.x, `Library` still sent the posthog-go version as `$lib_version`. A
`$lib` set alone now sends no `$lib_version`, so set `$lib_version` yourself
to keep a version on those events.
`posthogmcp` events report `posthog-go-mcp` with the SDK version, unless you
set `$lib` in the `ToolCall` or `EventContext` properties or the request
context. `DefaultEventProperties` can't change it, because `posthogmcp` sets
its pair before the defaults fill.

`$lib_version` now reports the SDK version from a Go test binary too. 1.x
reported `1.0.0` from any test binary, including your application's own.

`Config.DefaultEventProperties` are now defaults too. The SDK fills them in
before `BeforeSend`, only for keys that the event and the request context left
unset. A key set to `nil` counts as set, so it keeps the default out.
`RequestContext.Properties` fill in the same way and win over the defaults. In
1.x the default value overwrote the event's value, except when the event
turned person processing off.

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
`GroupIdentify` events too, not only to `Capture`. A
`$process_person_profile: false` there turns person processing off for these
events too (see [New: event options](#new-event-options)). For `Identify`,
`Alias` and `GroupIdentify` they fill the new `EventProperties` field: the
properties of the event itself, separate from the `$set` or `$group_set` that
`Properties` sends. A `$set` in `Identify.EventProperties` or a `$group_set` in
`GroupIdentify.EventProperties` merges under `Properties`, which win key by
key. The `Config.Callback` message for an `Alias` does not include its
`EventProperties`.

The values the SDK adds are defaults too. The SDK fills them before
`BeforeSend`, only for keys the caller left unset:

- `$is_server`, from `Config.IsServer`
- `$geoip_disable`, from `Config.DisableGeoIP`
- `$os`, `$os_version`, `$os_distro` and `$go_version`
- `$lib` and `$lib_version`, `posthog-go` and the SDK version, as a pair. They
  follow their own rule (see [Event properties](#event-properties)): a `nil`,
  empty or non-string `$lib` counts as unset, and a `$lib_version` set without
  a `$lib` is replaced
- `$feature/<key>` and `$active_feature_flags`, from a `Flags` snapshot or
  `SendFeatureFlags`

All of them fill last, under the event, the request context and
`DefaultEventProperties`. So a `$feature/<key>` set in the request context or
`DefaultEventProperties` wins over the flag value, and one set in the event's
own `Properties` wins over both.

In 1.x the SDK overwrote the values you set for `$is_server`,
`$geoip_disable`, `$lib`, `$lib_version` and the system properties. So a
`DefaultEventProperties` value of `$geoip_disable: false` now turns GeoIP
lookup back on for events, even when `Config.DisableGeoIP` is on. Feature flag
requests still follow `Config.DisableGeoIP`. The `IsServer` and `DisableGeoIP`
fields of a message report the values left after `BeforeSend`. To change
them, set the properties.

A minimal `$feature_flag_called` event, which the server can turn on for
flags without an experiment, is trimmed to its fixed set of properties before
`BeforeSend`. The hook sees only that set, and the properties it adds are
sent. In 1.x the trim ran after `BeforeSend` and removed them.

For the feature flag properties, the only change is that a `$feature/<key>`
set on the event or in the request context now wins over `SendFeatureFlags`.
A `Flags` snapshot already let them win, and `DefaultEventProperties` already
beat the flag values on both paths.

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

The event name limit is new: 1.x sent events to legacy capture, which had no
limit on event name length. Check your event names before you upgrade.

Each event needs its own uuid. When a request contains the same uuid more than
once, capture keeps the first event and drops the later copies. The SDK reports
each dropped copy to `Callback.Failure` as a `*CaptureEventError` with `Result`
`drop` and `Details` `duplicate_event_uuid`, and never retries it. Older capture
versions reject the whole request with a 400, which the SDK reports as a
`*CaptureRequestError` (`duplicate_event_uuid`).

### Non-finite numbers

A NaN or ±Inf property value, `float32` or `float64`, is sent as `null` and the
event is kept, as in 1.36.1 and later. A struct value that holds one is sent as
`null` as a whole. Each event is checked as JSON before it is queued, so a
custom `MarshalJSON` that returns invalid JSON fails only its own event, which
`Callback.Failure` reports.

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

Each option replaces a legacy option property:

| Option | Replaces |
| --- | --- |
| `process_person_profile` | `$process_person_profile` |
| `cookieless_mode` | `$cookieless_mode` |
| `disable_skew_correction` | `$ignore_sent_at` |
| `product_tour_id` | `$product_tour_id` |

Legacy option properties still work, but an option overrides its legacy
property. Use options.

Options can also be set for every event and for a request:

- `Config.DefaultEventOptions` applies to every `Capture`, `Exception`,
  `Identify`, `Alias` and `GroupIdentify` event, like
  `Config.DefaultEventProperties`.
- `RequestContext.Options` applies to every event sent with
  `EnqueueWithContext` or `EnqueueAIWithContext`, like
  `RequestContext.Properties`. A child context inherits its parent's options.

These layers reach `Identify`, `Alias` and `GroupIdentify` events too, and so
does a legacy `$process_person_profile` property in `DefaultEventProperties` or
`RequestContext.Properties`. 1.x never sent those values with these messages.
PostHog drops an `$identify`, `$create_alias` or `$groupidentify` event that
has person processing off, with an ingestion warning, so it never creates or
merges a person or updates a group. It keeps a `$set` event but ignores its
person update. `Callback` does not report the drop, because capture accepts the
event. To keep these events, set the option on the message, which wins over
every layer, or set it in `BeforeSend`:

```go
client.Enqueue(posthog.Identify{
    DistinctId: "user-123",
    Properties: posthog.NewProperties().Set("plan", "pro"),
    Options:    posthog.NewOptions().Set("process_person_profile", true),
})
```

A `$process_person_profile` property in the message's `EventProperties` does
not override `DefaultEventOptions` or `RequestContext.Options`.

The event's `Options` come first. Before `BeforeSend`, the SDK fills each
option that is still missing or `nil`, from these layers in order:
`RequestContext.Options`, then `DefaultEventOptions`, then the SDK's
personless default (below). So a `nil` event option still gets the default.
`BeforeSend` sees the filled options and can change or remove them. To turn a
default off for one event, set the option to `false` on the event or in
`BeforeSend`. To change an option in `BeforeSend`, set it in `Options`.

Review your `BeforeSend` hooks for the new message shape. A hook that returns
a new message must copy `Options`, or the event is sent without them. That
includes the personless default below, so PostHog would create a person for
the generated ID.

When a `Capture` or `Exception` with a request context has no distinct ID, the
SDK generates one and sets the option `process_person_profile: false`, so it
does not create a person for every generated ID. A default, request-context or
event option can turn processing back on. `posthogmcp` sets the same option on
events without an identity. This covers tool calls and the
`$mcp_unknown_tool`, `$mcp_input_required` and `$mcp_missing_capability`
events.

`posthogmcp.ToolCall` has a new `EventOptions` field, and tool calls also take
`RequestContext.Options`. The call's options override the request context's.
The other MCP events take the same layers through `EventContext.EventOptions`.
With `posthogmcpsdk`, `WithEventOptions` sets a tool call's `EventOptions`:

```go
posthogmcpsdk.Instrument(server, analytics,
    posthogmcpsdk.WithEventOptions(func(
        context.Context, *mcp.CallToolRequest, *mcp.CallToolResult, error,
    ) (posthog.Options, error) {
        return posthog.NewOptions().Set("process_person_profile", false), nil
    }),
)
```

`posthogmcp` sets `process_person_profile: false` on a call without an
identity, whatever its options say. `BeforeSend` runs after that and can still
change it, and PostHog receives what the hook returns.

`posthogmcp` events take `RequestContext.Properties` under the call's
properties with any client, as in 1.x, and `RequestContext.Options` under the
call's options in the same way. The call's values win, and the context's values win over
`DefaultEventProperties` and `DefaultEventOptions`. `posthogmcp` still drops
the reserved keys, such as `$mcp_*`, `$set` and `$session_id`, from the
context properties and sanitizes their values first.

Compared with 1.x, which sent these settings as properties to the `/batch/`
endpoint:

- PostHog now reads common forms of an option's value, so
  `posthog.NewOptions().Set("process_person_profile", "false")` turns person
  processing off. 1.x ignored the string `"false"` for this setting.
- A value PostHog cannot read, such as `"maybe"` for `process_person_profile`,
  now drops the event as `invalid_options`. 1.x kept the event and ignored the
  value, except for cookieless mode, where any non-boolean value failed the
  whole batch.

If you used `CaptureModeAnalyticsV1`, note that the SDK no longer converts
option values or silently removes the ones it cannot read. PostHog validates
them as described above.

Minimal `$feature_flag_called` events keep their options, because the trim
removes only properties. In 1.x the trim removed these settings, except person
processing.

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
Routing is by method: `Enqueue` never reroutes an `$ai_`-prefixed event.
Send `$ai_` events with `EnqueueAI` or `EnqueueAIWithContext`. PostHog
accepts `$ai_` events on the analytics endpoint for now, but this is
temporary. `EnqueueAI`
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
`CaptureAICompression` does not inherit `Compression`: AI requests are sent
uncompressed unless you set it. The AI lane does not use `MaxQueueSize` or
`BatchUploadTimeout` either. It shares `BatchSize`, `Interval`, `MaxRetries`,
`RetryAfter`, `MaxRetryBackoff`, `Callback`, `BeforeSend`,
`HistoricalMigration`, `DefaultEventProperties` and `DefaultEventOptions`
with the analytics lane.
Use `EnqueueAIWithContext` from HTTP handlers, as you would
`EnqueueWithContext`. It takes a `Client` rather than an `EnqueueClient`,
because only a real client has an AI lane.
