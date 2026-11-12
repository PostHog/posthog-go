# Migrate the Go SDK from 1.x.x to 2.0.0

The Go SDK 2.x.x sends every event to the [capture v1](https://posthog.com/docs/api/capture-v1) endpoints, `/i/v1/analytics/events` and `/i/v1/ai/events`.

Use the top right copy page button to work through this with your agents.

You don't need to make every change on the day you upgrade your SDK. Required changes fall in these categories:

- **Before you upgrade**: you should make these changes before you upgrade your SDK to 2.x.x.
- **During your upgrade**: These are changes that you need to make in the same PR that upgrades your SDK version.
- **Follow ups**: These are changes that can come in follow up PRs because 2.x.x has limited backward compatibility for methods that are to be deprecated.

## Checklist

Read the checklist first, then the sections that apply to you. Many of these changes don't produce a compile error, so check every row even if your code builds unchanged.

**Before you upgrade**

- [ ] You run self-hosted PostHog → [Prepare your proxy](#prepare-your-proxy)
- [ ] You send events through your own reverse proxy or API gateway → [Prepare your proxy](#prepare-your-proxy)
- [ ] An egress allowlist or WAF rule pins the `/batch/` path → [Prepare your proxy](#prepare-your-proxy)
- [ ] A proxy redirects capture requests → [Prepare your proxy](#prepare-your-proxy)
- [ ] Something inspects, logs, or rewrites the request body to find the API key → [Prepare your proxy](#prepare-your-proxy)
- [ ] You pass a personal API key (`phx_...`) as the project API key → [Use a project key](#use-a-project-key)
- [ ] You send event names longer than 200 bytes → [Check event names](#check-event-names)

**During your upgrade**

- [ ] You import `github.com/posthog/posthog-go`, `posthogmcp`, `posthogmcpsdk`, `posthogmcpgo`, or the `otel` bridge → [Change the import path](#change-the-import-path)
- [ ] You call `posthog.New` → [Handle constructor errors](#handle-constructor-errors)
- [ ] You compare the text of `ErrSDKDisabled` → [Handle constructor errors](#handle-constructor-errors)
- [ ] You pass a nil `*posthogmcp.Analytics` to `posthogmcpsdk`, or an empty API key to the `otel` bridge → [Handle constructor errors](#handle-constructor-errors)
- [ ] You set `Config.CaptureMode`, `Capture.Library`, or `Exception.Library` → [Fix removed APIs](#fix-removed-apis)
- [ ] You implement the `Client` interface, for example in a test mock → [Fix removed APIs](#fix-removed-apis)
- [ ] You compare `Alias` or `Backoff` values with `==`, or use them as map keys → [Fix removed APIs](#fix-removed-apis)
- [ ] You pass a value that isn't a string to `Groups.Set` → [Convert group keys](#convert-group-keys)
- [ ] You rely on the 500000-byte cap on one event → [Check config values](#check-config-values)
- [ ] You set a negative timeout, size, or queue value in `Config` → [Check config values](#check-config-values)
- [ ] You set `Config.Compression`, or something in your path expects the `?compression=` query parameter or the `lz64` or `base64` encodings → [Check config values](#check-config-values)
- [ ] Your `BeforeSend` hook returns a new message → [Copy options in BeforeSend](#copy-options-in-beforesend)
- [ ] You send an empty or whitespace-only `DistinctId` or `Alias` → [Fix identity calls](#fix-identity-calls)
- [ ] You send `Identify`, `Alias`, or `GroupIdentify` with `EnqueueWithContext` → [Fix identity calls](#fix-identity-calls)
- [ ] You set `GroupIdentify.DistinctId` → [Fix identity calls](#fix-identity-calls)
- [ ] You turn person processing off in `DefaultEventOptions`, `DefaultEventProperties`, `RequestContext.Options`, or `RequestContext.Properties` and send `$identify`, `$create_alias`, `$merge_dangerously`, `$groupidentify`, `$set`, or `$set_once` events → [Check identify-style events](#check-identify-style-events)
- [ ] Your `BeforeSend` hook builds or renames a `$identify`, `$create_alias`, `$merge_dangerously`, `$groupidentify`, `$set`, or `$set_once` event → [Check identify-style events](#check-identify-style-events)
- [ ] You set `Uuid` yourself, or reuse one `Uuid` for more than one event → [Check event IDs](#check-event-ids)
- [ ] You expect `DefaultEventProperties` to override an event's properties, including `$set` and `$groups` → [Check property precedence](#check-property-precedence)
- [ ] You set `$is_server`, `$geoip_disable`, or a system property such as `$os`, and expect the SDK's value to win → [Check property precedence](#check-property-precedence)
- [ ] Your `BeforeSend` hook adds properties to `$feature_flag_called` events → [Check property precedence](#check-property-precedence)
- [ ] You set `$trace_id` or `$span_id` and use `posthogotel.EnqueueWithContext` → [Check property precedence](#check-property-precedence)
- [ ] You set `$lib` or `$lib_version` yourself → [Check SDK identity](#check-sdk-identity)
- [ ] Your `Callback.Failure` compares errors with `==`, or expects only failed requests → [Update error handling](#update-error-handling)
- [ ] You alert on SDK log lines, or read messages from the `Verbose` log → [Update error handling](#update-error-handling)
- [ ] You set a custom `Config.RetryAfter`, or call `DefaultBackoff()` → [Update error handling](#update-error-handling)
- [ ] You set `$session_id` or `$window_id` to a value that isn't a string → [Check dropped events](#check-dropped-events)
- [ ] You send `$performance_event`, `float32` values, or types with a custom `MarshalJSON` → [Check dropped events](#check-dropped-events)
- [ ] You read the text of event timestamps → [Check timestamps](#check-timestamps)

**Follow ups**

- [ ] You set legacy option properties like `$process_person_profile` → [Move to event options](#move-to-event-options)
- [ ] You send `$ai_` events with `Enqueue` → [Send AI events with EnqueueAI](#send-ai-events-with-enqueueai)
- [ ] You use `posthogmcp` or `posthogmcpsdk` → [Add MCP event options](#add-mcp-event-options)

## Before you upgrade

### Prepare your proxy

2.x.x sends events to these endpoints:

| Method | Endpoint |
| --- | --- |
| `Enqueue`, `EnqueueWithContext` | `POST /i/v1/analytics/events` |
| `EnqueueAI`, `EnqueueAIWithContext` | `/i/v1/ai/events` |

1.x.x sent `Enqueue` and `EnqueueWithContext` events to `POST /batch/`.

Check anything that pins the old path: reverse proxies, API gateways, egress allowlists, WAF rules, and self-hosted deployments.

The v1 routes are only served where the deployment enables them. Confirm your PostHog instance serves both `/i/v1/analytics/events` and `/i/v1/ai/events` before you upgrade.

On self-hosted PostHog, a separate capture service serves the AI route, so check it too if you use `EnqueueAI`. PostHog Cloud (`us.i.posthog.com`, `eu.i.posthog.com`) serves both.

If you send events through your own reverse proxy, update it before you upgrade:

- **Forward both routes.** Forward `/i/v1/analytics/events` and `/i/v1/ai/events` to PostHog.
- **Pass the headers through unchanged.** Pass `Authorization`, every `PostHog-*` header, `Content-Type`, `Content-Encoding`, and `User-Agent`. `Authorization` holds the project API key, sent as `Bearer <key>`. PostHog answers `401` without `Authorization` and `400` without the others, and the batch fails.
- **Keep the credentials.** If your proxy strips credentials, keep an `Authorization` header that starts with `Bearer phc_`, `Bearer phs_`, or `Bearer phx_`. PostHog SDKs send their keys there. Keys created before these prefixes need their exact `Bearer <key>` value allowed too.
- **Accept request bodies up to 20 MB.** AI requests are often larger than 1 MB, the default limit in nginx and ingress-nginx. A `413` from the proxy fails the batch.
- **Pass the capture response through.** A proxy or gateway that answers the capture path with its own `200` body shows up as failures (see [Update error handling](#update-error-handling)).

PostHog's managed reverse proxy already does this. See the [proxy guides](https://posthog.com/docs/advanced/proxy).

The project API key moves from an `api_key` body field to an `Authorization: Bearer <key>` header. Update anything that inspected, logged, or rewrote the request body to find the key.

A capture request follows only `307` and `308` redirects, and only to the same scheme, host, and port as `Endpoint`, at most 5 times. It resends the same body and headers.

Any other redirect fails the batch without a retry. `Callback.Failure` gets a `CaptureRequestError` with the redirect's status.

1.x.x followed up to 10 redirects of any kind to any host:

- **A `301`, `302`, or `303`.** 1.x.x resent the request as a `GET` without the events.
- **A `307` or `308` to another host.** That host received the events and the API key.

Feature flag requests are unchanged.

### Use a project key

A personal API key (`phx_...`) passed as the project API key gives a disabled client and a nil error, as an empty key does. Both log an error.

A disabled client sends nothing (see [Handle constructor errors](#handle-constructor-errors)).

### Check event names

PostHog drops an event whose name is longer than 200 bytes and accepts the rest of the batch. The drop reaches `Callback.Failure` as a `*CaptureEventError`.

1.x.x sent events to legacy capture, which had no limit on event name length. Check your event names before you upgrade.

## During your upgrade

### Change the import path

Go requires the major version in the module path:

```bash
go get github.com/posthog/posthog-go/v2
```

The package name is still `posthog`, so only the import line changes:

```go
// before
import "github.com/posthog/posthog-go"

// after
import "github.com/posthog/posthog-go/v2"
```

The other packages move with the core:

| Package | 2.x.x import path |
| --- | --- |
| Subpackages such as `posthogmcp` | `github.com/posthog/posthog-go/v2/posthogmcp` |
| The MCP Go SDK adapter | `github.com/posthog/posthog-go/posthogmcpsdk/v2` |
| The mcp-go adapter | `github.com/posthog/posthog-go/posthogmcpgo/v2` |
| The `otel` bridge | `github.com/posthog/posthog-go/otel/v2` |

The MCP Go SDK adapter and the mcp-go adapter are separate modules that move in lockstep with the core.

The `otel` bridge imports the core SDK, so it also moves in lockstep with the core.

### Handle constructor errors

`New` returns `(Client, error)`, like `NewWithConfig`. Handle the error before you use or close the client:

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

Either constructor returns a nil client and an error only for invalid configuration.

An empty project API key still gives a disabled client and a nil error, as in 1.x.x. A personal (`phx_...`) key does too in 2.x.x. Both log an error.

- **What a disabled client does.** It sends nothing, and its methods return default values and `ErrSDKDisabled`.
- **The error text.** The text of `ErrSDKDisabled` is `posthog SDK is disabled`. If your code compares error strings, use `errors.Is(err, posthog.ErrSDKDisabled)` instead.
- **A disabled client on purpose.** Set `Config.Disabled` to build one, for example in tests.

Integrations follow the same rule:

- **`posthogmcpsdk`.** `NewMiddleware` and `Instrument` no longer panic on a nil `*posthogmcp.Analytics`. They report one error and install middleware that changes nothing.
- **The `otel` bridge.** `NewExporter` and `NewSpanProcessor` no longer return an error for an empty or personal API key. They log one error and return an exporter or processor that exports nothing. They still return an error for an invalid `WithHost`.

### Fix removed APIs

2.x.x removes or changes these APIs:

| API | Change |
| --- | --- |
| `Config.CaptureMode` and the `CaptureMode` type | Removed |
| The `CaptureModeLegacy` and `CaptureModeAnalyticsV1` constants | Removed |
| `Capture.Library` and `Exception.Library` (added in 1.30.0) | Removed |
| `Client` | New `EnqueueAI` method |
| `Alias` | New `EventProperties` and `Options` maps |
| `Backoff` | New function field |
| `New` | Returns `(Client, error)` |
| `Groups` | A `map[string]string` |

Capture v1 is the only behavior. If you opted into capture v1, delete the `Config.CaptureMode` field.

In place of `Capture.Library` and `Exception.Library`, set `$lib` in the event's properties (see [Check SDK identity](#check-sdk-identity)).

The new `Client` method only affects code that implements the interface, so a hand-written test mock needs the new method. Callers are unaffected.

You can't compare an `Alias` or a `Backoff` with `==` or use it as a map key, so code that does this no longer compiles.

For `New`, see [Handle constructor errors](#handle-constructor-errors). For `Groups`, see [Convert group keys](#convert-group-keys).

Delete `CaptureMode` from your config:

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

If you used `CaptureModeAnalyticsV1`, the SDK no longer converts option values or silently removes the ones it can't read. PostHog validates them (see [Move to event options](#move-to-event-options)).

### Convert group keys

`Groups` is `map[string]string`, and `Groups.Set` takes a string value. A group key identifies a group, so numbers, maps, and other values no longer compile. This matches `GroupIdentify.Key`, which was already a string.

Convert a numeric group ID to its decimal string:

```go
// before
groups := posthog.NewGroups().Set("company", companyID) // companyID is an int64

// after
groups := posthog.NewGroups().Set("company", strconv.FormatInt(companyID, 10))
```

1.x.x bucketed a numeric group key for feature flags by its decimal form. A key converted this way keeps every group in the same rollout bucket and variant.

### Check config values

`Config` has three new size and retry fields:

| Field | Default |
| --- | --- |
| `MaxBatchBytes` | 500000 bytes |
| `MaxEventBytes` | No cap |
| `MaxRetryBackoff` | 30s |

`MaxBatchBytes` targets the combined serialized size of the events in one request, excluding the request envelope. Its default is the request limit 1.x.x applied internally.

An event larger than `MaxBatchBytes` is sent in a request of its own.

`MaxEventBytes` refuses large events locally with `ErrMessageTooBig`. With it set, `Validate`, and so `NewWithConfig`, rejects a `MaxBatchBytes` below it.

`MaxRetryBackoff` caps `Retry-After` and the default exponential backoff (see [Update error handling](#update-error-handling)).

There's no per-event size cap by default. 1.x.x had a 500000-byte cap.

Within PostHog's request limits, PostHog drops an oversized event on its own and reports it to `Callback`. The other events in the request don't fail.

`MaxEventBytes` and `MaxBatchBytes` don't apply to `EnqueueAI` (see [Send AI events with EnqueueAI](#send-ai-events-with-enqueueai)).

`Validate` rejects a negative `BatchUploadTimeout`, `CaptureAIBatchUploadTimeout`, `MaxRetryBackoff`, `MaxEventBytes`, `MaxBatchBytes`, or `CaptureAIMaxQueueSize`. Only zero selects the default.

The compression settings change in three ways:

- **No opt-in.** `CompressionZstd`, `CompressionDeflate`, and `CompressionBrotli` no longer require an opt-in. A config that failed `Validate()` with `"zstd compression requires CaptureModeAnalyticsV1"` succeeds.
- **Encodings.** `Content-Encoding` must be one of `gzip`, `deflate`, `br`, or `zstd`. The legacy `lz64` and `base64` encodings and the `?compression=` query parameter are gone.
- **Codec failures.** If a configured codec fails at runtime, the SDK still sends the batch, uncompressed.

### Copy options in BeforeSend

Review your `BeforeSend` hooks for the new message shape. Capture v1 reads per-event processing controls from an `options` object sent next to `properties`, and every message type has an `Options` field (see [Move to event options](#move-to-event-options)).

A hook that returns a new message must copy `Options`, or the event is sent without them. That includes the SDK's personless default, so PostHog would create a person for the generated ID.

- **The personless default.** When a `Capture` or `Exception` with a request context has no distinct ID, the SDK generates one and sets the option `process_person_profile: false`. So it doesn't create a person for every generated ID.
- **Turn processing back on.** A default, request-context, or event option can turn processing back on.
- **MCP events.** `posthogmcp` sets the same option on events without an identity. This covers tool calls and the `$mcp_unknown_tool`, `$mcp_input_required`, and `$mcp_missing_capability` events.
- **Options in the hook.** A `BeforeSend` hook can read and change `Options` like `Properties`, and `Options` is never nil inside the hook. To change an option in `BeforeSend`, set it in `Options`. The SDK checks the event name the call sends, so a hook that builds or renames an identify-style event must set the option itself (see [Check identify-style events](#check-identify-style-events)).

### Fix identity calls

- **Empty IDs.** An empty or whitespace-only `DistinctId` counts as missing. A `Capture` or `Exception` sent with a request context gets the context's ID or a personless one. 1.x.x sent it, and PostHog dropped the event.
- **Whitespace-only IDs without a request context.** `Enqueue` returns a `FieldError` for a `Capture` or `Exception` whose `DistinctId` is only whitespace. 1.x.x sent these events.
- **Whitespace-only `Identify` and `Alias`.** With or without a request context, `Enqueue` returns a `FieldError` for an `Identify` or `Alias` whose `DistinctId` is only whitespace. It does the same for an `Alias` whose `Alias` is only whitespace. 1.x.x sent these events.
- **Long distinct IDs.** PostHog drops an event whose distinct ID is over 200 UTF-8 bytes, with the per-event result `distinct_id_too_large`. It reaches `Callback.Failure` as a `CaptureEventError`. 1.x.x sent events to legacy capture, which shortened the ID to 200 characters and kept the event.
- **Group distinct ID.** `GroupIdentify.DistinctId` is sent as the event's distinct ID. If it's empty, the SDK uses the request context's distinct ID, then `$<type>_<key>`. 1.x.x accepted and ignored the field, and sent every `$groupidentify` as `$<type>_<key>`.
- **Long group IDs.** If `$<type>_<key>` is over 200 bytes, set `DistinctId`, or PostHog drops the event.
- **Request context values.** `RequestContext.Properties` and `RequestContext.Options` apply to `Identify`, `Alias`, and `GroupIdentify` events sent with `EnqueueWithContext`. In 1.x.x they applied only to `Capture` and `Exception`. They never set person processing on identify-style events whose distinct ID the SDK didn't generate (see [Check identify-style events](#check-identify-style-events)).
- **Context session ID.** The context's session ID fills `Identify` and `Alias`, but never `GroupIdentify`. A session's person in PostHog follows the distinct IDs of the session's events. So an event sent under a different distinct ID with the context's session ID can change the person shown for that session.
- **Context distinct ID.** The context's distinct ID never changes an `Identify` or an `Alias`.

### Check identify-style events

Identify-style events are `$identify`, `$create_alias`, `$merge_dangerously`, `$groupidentify`, `$set`, and `$set_once`. They're the events that `Identify`, `Alias`, and `GroupIdentify` send, and a `Capture` with one of these names.

They exist to create, link, or change a person or group, and with person processing off PostHog drops them or ignores their person update. So they default to `process_person_profile: true` unless the call itself sets the option or the legacy `$process_person_profile` property:

- **The call's value.** The call's value wins over every layer. A `false` in `DefaultEventOptions`, `RequestContext.Options`, `DefaultEventProperties`, or `RequestContext.Properties` doesn't reach these events, unless the SDK generates the distinct ID (below).
- **A legacy property on the call.** A legacy `$process_person_profile` in the call's `Properties` (for `Capture`) or `EventProperties` becomes the option before the layers fill, and the property is removed. If the call also sets the option, the option wins.
- **`nil` values.** A `nil` option or legacy property counts as unset.
- **Generated distinct IDs.** A `Capture` whose distinct ID the SDK generates gets no `true`. Its `process_person_profile` comes from the layers, then the personless `false` (see [Copy options in BeforeSend](#copy-options-in-beforesend)), unless the call sets the option or the legacy property.
- **Layered legacy properties.** `BeforeSend` sees the option without a legacy `$process_person_profile` from `DefaultEventProperties` or `RequestContext.Properties`, so a hook that clears `Options` doesn't send a `false` from a layer.
- **`BeforeSend`.** The hook sees the value and can change it. The SDK checks the event name the call sends, so a `BeforeSend` hook that builds or renames an identify-style event must set the option itself.
- **Keeping one personless.** To send an identify-style event without person processing, set `process_person_profile` to `false` on the message or in `BeforeSend`, or turn off person processing for the project in PostHog.

### Check event IDs

- **Generated IDs.** The SDK generates UUIDv7 values for event `Uuid`s, the `PostHog-Request-Id` header, and the distinct ID of a personless capture. It falls back to v4 if v7 generation fails.
- **Your own `Uuid`.** The SDK normalizes a `Uuid` you set to the lowercase hyphenated form, so the `Callback` message carries that form.
- **Empty values.** An empty `Uuid` gets a generated one.
- **Invalid values.** The SDK replaces an invalid `Uuid` with a generated one. It logs one warning that doesn't include the value.
- **`BeforeSend`.** The SDK applies this rule before `BeforeSend`, and again to a `Uuid` that `BeforeSend` sets.
- **Duplicates.** Each event needs its own uuid. When a request contains the same uuid more than once, capture keeps the first event and drops the later copies. The SDK reports each dropped copy to `Callback.Failure` as a `*CaptureEventError` with `Result` `drop` and `Details` `duplicate_event_uuid`, and never retries it. Older capture versions reject the whole request with a `400`, which the SDK reports as a `*CaptureRequestError` with `duplicate_event_uuid`.

### Check property precedence

An event's properties and options come from five steps, in this order. Steps 2 to 4 fill only the properties and options that the steps before them left unset:

1. The event's own `Properties` and `Options`. The request context's session ID is part of the event's identity, so it's set as `$session_id` first, unless the event has its own.
2. `RequestContext.Properties` and `RequestContext.Options`.
3. `Config.DefaultEventProperties` and `Config.DefaultEventOptions`.
4. Values the SDK sets:
   - `$is_server`, from `Config.IsServer`
   - `$geoip_disable`, from `Config.DisableGeoIP`
   - `$os`, `$os_version`, `$os_distro`, and `$go_version`
   - `$lib` and `$lib_version`, `posthog-go` and the SDK version, as a pair (see [Check SDK identity](#check-sdk-identity))
   - `$feature/<key>` and `$active_feature_flags`, from a `Flags` snapshot or `SendFeatureFlags`
   - The option `process_person_profile: false` for a personless event (see [Copy options in BeforeSend](#copy-options-in-beforesend))
5. `BeforeSend`, which sees the result of steps 1 to 4 and can change or remove any of it. Its changes are final.

A property key set to `nil` counts as set, so it keeps the default out. An option that's missing or `nil` counts as unset, so a `nil` event option still gets the default.

Steps 2 to 4 don't fill `process_person_profile` on an identify-style event that already defaulted to `true` (see [Check identify-style events](#check-identify-style-events)).

`$lib` and `$lib_version` follow their own rule. A `nil`, empty, or non-string `$lib` counts as unset, and the SDK replaces a `$lib_version` set without a `$lib`.

To turn a default option off for one event, set the option to `false` on the event or in `BeforeSend`.

`$set`, `$set_once`, `$groups`, and `$group_set` fill one level deep. When the event and a default both set one of them to a map, the default adds only the keys the event left out.

For example, a default `"$set": {"plan": "free", "source": "web"}` and an event `"$set": {"plan": "pro"}` send `{"plan": "pro", "source": "web"}`.

These changes follow from this order:

- **Default properties.** `Config.DefaultEventProperties` fill only the keys that the event and the request context left unset. In 1.x.x the default value overwrote the event's value, except when the event turned person processing off.
- **Merged maps.** A `$set`, `$set_once`, `$groups`, or `$group_set` default no longer replaces the event's whole map. In 1.x.x it did.
- **`Capture.Groups`.** It merges into a `$groups` map in `Properties` and wins key by key. In 1.x.x it replaced the map.
- **Message types.** `DefaultEventProperties` apply to `Exception`, `Identify`, `Alias`, and `GroupIdentify` events. In 1.x.x they applied only to `Capture`. A `$process_person_profile: false` there turns person processing off for `Exception` events too, but not for identify-style events (see [Check identify-style events](#check-identify-style-events)).
- **`EventProperties`.** For `Identify`, `Alias`, and `GroupIdentify`, the defaults fill the new `EventProperties` field. It holds the properties of the event itself, separate from the `$set` or `$group_set` that `Properties` sends.
- **`$set` in `EventProperties`.** A `$set` in `Identify.EventProperties` or a `$group_set` in `GroupIdentify.EventProperties` merges under `Properties`, which win key by key.
- **`Alias` callbacks.** The `Config.Callback` message for an `Alias` doesn't include its `EventProperties`.
- **SDK values.** A `$is_server`, `$geoip_disable`, `$lib`, `$lib_version`, or system property that you set wins over the SDK's value. In 1.x.x the SDK overwrote it.
- **GeoIP.** A `DefaultEventProperties` value of `$geoip_disable: false` turns GeoIP lookup back on for events, even when `Config.DisableGeoIP` is on. Feature flag requests still follow `Config.DisableGeoIP`.
- **Message fields.** The `IsServer` and `DisableGeoIP` fields of a message report the values left after `BeforeSend`. To change them, set the properties.
- **Feature flag properties.** A `$feature/<key>` set in the request context or `DefaultEventProperties` wins over the flag value. One set in the event's own `Properties` wins over both.
- **Feature flag properties and `SendFeatureFlags`.** The only change from 1.x.x is that a `$feature/<key>` set on the event or in the request context wins over `SendFeatureFlags`. A `Flags` snapshot already let them win, and `DefaultEventProperties` already beat the flag values on both paths.
- **Minimal flag events.** The server can turn on a minimal `$feature_flag_called` event for flags without an experiment. The SDK trims it to its fixed set of properties before `BeforeSend`. The hook sees only that set, and the properties it adds are sent. In 1.x.x the trim ran after `BeforeSend` and removed them.
- **Options on minimal flag events.** Minimal `$feature_flag_called` events keep their options, because the trim removes only properties. In 1.x.x the trim removed these settings, except person processing.

`posthogotel.EnqueueWithContext` adds the active span's `$trace_id` and `$span_id` only when neither the event nor the request context sets either key. In 1.37.x each ID filled on its own, so an event that set only `$trace_id` got the active span's `$span_id`.

The helper can't see `Config.DefaultEventProperties`. A `$trace_id` or `$span_id` set there doesn't stop the derived pair, which wins over it.

### Check SDK identity

Every event carries `$lib` and `$lib_version` in its properties. By default they're `posthog-go` and the SDK version. PostHog stores the values the event is sent with.

You can set them in the event, the request context, or `DefaultEventProperties`. Like any other property, each one comes from the most specific place that sets it, so set both in the same place.

- **No `$lib`.** If no `$lib` is set to a non-empty string, the SDK sets both and replaces a `$lib_version` set on its own.
- **`posthog-go` without a version.** If `$lib` is `posthog-go` with no version, the SDK adds its version.
- **Any other `$lib`.** The SDK doesn't add its version. A `$lib` set alone sends no `$lib_version`, so set `$lib_version` yourself to keep a version on those events.
- **`BeforeSend`.** The hook sees both and can change or remove them.

If an event has no `$lib`, PostHog sets both from the `PostHog-Sdk-Info` header, `posthog-go/<version>`. In 1.x.x the default `/batch/` path overwrote a `$lib` you set.

`Capture.Library` and `Exception.Library` are removed. Set `$lib` in the event's properties instead.

For example, `Capture{..., Library: "my-lib"}` becomes `Capture{..., Properties: posthog.NewProperties().Set("$lib", "my-lib").Set("$lib_version", "1.2.3")}`. In 1.x.x, `Library` still sent the posthog-go version as `$lib_version`.

`posthogmcp` events report `posthog-go-mcp` with the SDK version, unless you set `$lib` in the `ToolCall` or `EventContext` properties or the request context. `DefaultEventProperties` can't change it, because `posthogmcp` sets its pair before the defaults fill.

`$lib_version` reports the SDK version from a Go test binary too. 1.x.x reported `1.0.0` from any test binary, including your application's own.

### Update error handling

Capture v1 returns a result for each event, and the client resends only the events the backend asks it to retry. The old endpoint accepted or rejected a whole batch.

`Callback` changes with it:

- **Failures on a `200`.** `Failure` can fire on an HTTP 200, for an individual event the backend dropped while it accepted the rest of the batch.
- **Unreadable responses.** A `2xx` whose body isn't JSON, or whose `results` is missing, `null`, or not an object, fails the whole batch. Each event reaches `Failure` with a `*CaptureRequestError` and isn't resent. The old endpoint treated any status below 300 as success.
- **Missing results.** An event whose uuid is absent from a valid `results` object fires neither callback.

Errors are typed. Check them with `errors.As`:

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

`Failure` also gets local failures, and it can run on the goroutine that called `Enqueue`:

- **Local failures.** A local failure, such as an event over `MaxEventBytes` or a full batch-submission queue, reaches `Failure` wrapped in a `*CaptureLocalError`. In 1.x.x these were the bare sentinel errors, so `err == posthog.ErrMessageTooBig` no longer matches. Use `errors.Is(err, posthog.ErrMessageTooBig)`, which works in both.
- **A full message queue.** `Enqueue` returns `ErrQueueFull` and `Failure` isn't called, so check the error `Enqueue` returns.
- **The lane.** The new `Endpoint` field on all three error types names the lane: the analytics path or the AI path.
- **The calling goroutine.** `Failure` can run on the goroutine that called `Enqueue`. A message that fails JSON serialization is reported before `Enqueue` returns. This was also true in 1.x.x.
- **Re-enqueues.** Don't re-enqueue the same message from `Failure`. It fails again and the calls recurse.

Update log-based alerts for these log lines:

- **Without a `Callback`.** The SDK logs one warning each time a request loses events, never one per event. For example, `analytics: 3 event(s) dropped: <cause>; set Config.Callback to inspect failures`.
- **The cause.** For a failed request the cause is the error text, which can include PostHog's error description. 1.x.x logged the response body.
- **After the last retry.** The SDK logs events that still fail after the last retry as an error, with or without a `Callback`. For example, `analytics: 3 messages dropped after 4 attempts`.
- **Lane names.** Capture log lines start with the lane name, `analytics:` or `capture-ai:`, and some of their text changed from 1.x.x.
- **`Verbose`.** With `Verbose` on, 1.x.x logged every queued message in full. 2.x.x logs batch counts and sizes, not the messages.

2.x.x changes which statuses it retries and adds jitter to retry waits:

| Behavior | 1.x.x | 2.x.x |
| --- | --- | --- |
| Statuses retried | Every `5xx`, `408`, `429`, and network errors, but not `404` | `404`, `408`, `429`, `500`, `502`, `503`, `504`, and network errors |
| Default retry waits | No jitter | Up to 25% jitter |

In 2.x.x, other `5xx` statuses are terminal.

A `404` that lasts through every attempt can mean the PostHog deployment has no capture v1 route, for example an older self-hosted install.

The default waits vary randomly by up to 25% either way, so clients that fail together don't all retry at the same moment.

`DefaultBackoff()` has the same jitter. In 1.x.x it had none.

Billing limits arrive as a terminal `402`.

The SDK honors `Retry-After`, clamped to the new `Config.MaxRetryBackoff`. The default is 30s, and it also caps the default exponential backoff. A server `Retry-After` stays the minimum wait.

A custom `Config.RetryAfter` is used as given.

### Check dropped events

PostHog rejects these events one by one inside an otherwise successful batch. Each one reaches `Callback.Failure` as a `*CaptureEventError`:

- Event names longer than 200 bytes (see [Check event names](#check-event-names))
- Distinct IDs longer than 200 bytes
- `$performance_event`
- Properties that aren't a JSON object
- An event option value PostHog can't read, where `Details` is `invalid_options` (see [Move to event options](#move-to-event-options))

The SDK sends `$session_id` and `$window_id` as top-level fields of the event, because capture reads them there and requires strings. What the SDK sends depends on the value:

| Value | 2.x.x |
| --- | --- |
| A value whose JSON form is a string | Sent as that string |
| `nil` | Dropped, without a warning |
| Any other value | Dropped, with a warning |

A value whose JSON form is a string is a `string`, including `""`, a named string type, or a `uuid.UUID`.

`nil` counts as unset. For any other value, the SDK logs a warning for each drop that names the key and the value's type, never the value.

In 1.x.x the SDK sent any value as a property.

Non-finite numbers follow these rules:

- **`float32` and `float64` values.** A NaN or infinite property value is sent as `null` and the event is kept, as in 1.36.1 and later.
- **Structs.** A struct value that holds one is sent as `null` as a whole.
- **Invalid JSON.** Each event is checked as JSON before it's queued. A custom `MarshalJSON` that returns invalid JSON fails only its own event, and `Callback.Failure` reports it.

### Check timestamps

The SDK sends the event `timestamp`, the batch `created_at`, and the `PostHog-Request-Timestamp` header in UTC with at least millisecond digits, for example `2025-01-02T03:04:05.000Z`.

The SDK keeps finer precision from your `Timestamp` or the clock, up to nanoseconds.

1.x.x dropped trailing zeros from event timestamps, so a timestamp on a whole second had no fraction.

## Follow ups

### Move to event options

Capture v1 reads per-event processing controls, such as person profile processing, from an `options` object sent next to `properties`. Every message type (`Capture`, `Identify`, `Alias`, `GroupIdentify`, and `Exception`) has an `Options` field, and it works the same with `Enqueue` and `EnqueueAI`:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "user-123",
    Event:      "signed_up",
    Options:    posthog.NewOptions().Set("process_person_profile", false),
})
```

Keys are strings, and values are any JSON-serializable value.

- **Sent unchanged.** The SDK sends options unchanged, so an option that PostHog adds later needs no SDK upgrade.
- **Unknown keys.** PostHog validates options. It ignores keys it doesn't know.
- **Common forms.** PostHog reads common forms of a known key's value. For a boolean, that includes `"yes"`, `"off"`, or `0`.
- **Unreadable values.** PostHog drops the event if a known key has a value it can't read. That drop reaches `Callback.Failure` as a `*CaptureEventError` whose `Details` is `invalid_options`.

Each option replaces a legacy option property:

| Option | Replaces |
| --- | --- |
| `process_person_profile` | `$process_person_profile` |
| `cookieless_mode` | `$cookieless_mode` |
| `disable_skew_correction` | `$ignore_sent_at` |
| `product_tour_id` | `$product_tour_id` |

Legacy option properties still work, but an option overrides its legacy property. Use options.

You can also set options for every event and for a request:

- **Every event.** `Config.DefaultEventOptions` applies to every `Capture`, `Exception`, `Identify`, `Alias`, and `GroupIdentify` event, like `Config.DefaultEventProperties`.
- **One request.** `RequestContext.Options` applies to every event sent with `EnqueueWithContext` or `EnqueueAIWithContext`, like `RequestContext.Properties`. A child context inherits its parent's options.

The event's `Options` come first. Before `BeforeSend`, the SDK fills each option that's still missing or `nil`, from `RequestContext.Options`, then `DefaultEventOptions`, then the SDK's personless default (see [Check property precedence](#check-property-precedence)). It doesn't fill `process_person_profile` on an identify-style event that already defaulted to `true` (see [Check identify-style events](#check-identify-style-events)).

1.x.x sent these settings as properties to the `/batch/` endpoint. Two values behave differently as options:

| Value | 2.x.x | 1.x.x |
| --- | --- | --- |
| The string `"false"` for `process_person_profile` | Person processing off | Ignored |
| A value PostHog can't read | Event dropped as `invalid_options` | Event kept, value ignored |

PostHog reads common forms of an option's value, so `posthog.NewOptions().Set("process_person_profile", "false")` turns person processing off. 1.x.x ignored the string `"false"` for this setting.

A value PostHog can't read is one such as `"maybe"` for `process_person_profile`. PostHog drops the event as `invalid_options`.

1.x.x kept the event and ignored the value, except for cookieless mode, where any non-boolean value failed the whole batch.

### Send AI events with EnqueueAI

Send `$ai_` events with `EnqueueAI` or `EnqueueAIWithContext`. PostHog accepts `$ai_` events on the analytics endpoint for now, but this is temporary.

`EnqueueAI` sends to PostHog's dedicated AI capture endpoint, which accepts much larger events than the analytics endpoint:

```go
client.EnqueueAI(posthog.Capture{
    DistinctId: "user-1",
    Event:      "$ai_generation",
    Properties: props,
})
```

- **Its own lane.** `EnqueueAI` runs on its own queue, batching, and retry state, started on first use.
- **Routing.** Routing is by method. `Enqueue` never reroutes an `$ai_`-prefixed event.
- **Message types.** `EnqueueAI` accepts every message type and does no routing check of its own. Capture decides, and returns a per-event drop for an event the AI endpoint doesn't take.
- **Flushes.** `Flush` and `FlushWithContext` also wait for the AI lane once `EnqueueAI` has started it.
- **Size limits.** The AI lane's size limits are fixed, because they track the endpoint. The SDK refuses an event over 8 MiB plus 64 KiB of envelope headroom locally, and a batch closes at about 5 MiB. `MaxEventBytes` and `MaxBatchBytes` don't apply to it.
- **HTTP handlers.** Use `EnqueueAIWithContext` from HTTP handlers, as you would `EnqueueWithContext`. It takes a `Client` rather than an `EnqueueClient`, because only a real client has an AI lane.

Tune the AI lane with these `Config` fields:

| Field | In place of |
| --- | --- |
| `CaptureAICompression` | `Compression` |
| `CaptureAIMaxQueueSize` | `MaxQueueSize` |
| `CaptureAIBatchUploadTimeout` | `BatchUploadTimeout` |

`CaptureAICompression` doesn't inherit `Compression`. AI requests are sent uncompressed unless you set it.

The AI lane doesn't use `MaxQueueSize` or `BatchUploadTimeout`.

`CaptureAIBatchUploadTimeout` is 30s by default, longer than the analytics lane's because AI batches are much larger.

The AI lane shares `BatchSize`, `Interval`, `MaxRetries`, `RetryAfter`, `MaxRetryBackoff`, `Callback`, `BeforeSend`, `HistoricalMigration`, `DefaultEventProperties`, and `DefaultEventOptions` with the analytics lane.

### Add MCP event options

MCP events take options through `EventOptions` fields and `RequestContext.Options`:

- **Tool calls.** `posthogmcp.ToolCall` has a new `EventOptions` field, and tool calls also take `RequestContext.Options`. The call's options override the request context's.
- **Other MCP events.** They take the same layers through `EventContext.EventOptions`.
- **Calls without an identity.** `posthogmcp` sets `process_person_profile: false` on a call without an identity, whatever its options say. `BeforeSend` runs after that and can still change it, and PostHog receives what the hook returns.
- **Request context values.** `posthogmcp` events take `RequestContext.Properties` under the call's properties with any client, as in 1.x.x. They take `RequestContext.Options` under the call's options in the same way.
- **Precedence.** The call's values win, and the context's values win over `DefaultEventProperties` and `DefaultEventOptions`.
- **Reserved keys.** `posthogmcp` still drops the reserved keys, such as `$mcp_*`, `$set`, and `$session_id`, from the context properties and sanitizes their values first.

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
