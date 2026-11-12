# Getting started

The Go SDK runs on your server, on Go 1.21 or later.

To send your first event, create a client with your project token and host from your [project settings](https://app.posthog.com/project/settings), then call `Enqueue` with a `Capture`.

The client queues events and sends them in batches, so call `Close` before your program exits, or the last events are lost.

Install the module (Go 1.21 or later):

```bash
go get github.com/posthog/posthog-go
```

Create a client with your project API key, send an event, and close the client to flush the queue:

```go
package main

import "github.com/posthog/posthog-go"

func main() {
	client, _ := posthog.NewWithConfig("<ph_project_api_key>", posthog.Config{
		Endpoint: "<ph_client_api_host>",
	})
	defer client.Close() // flushes queued events

	client.Enqueue(posthog.Capture{
		DistinctId: "user_123",
		Event:      "button_clicked",
		Properties: posthog.NewProperties().Set("plan", "pro"),
	})
}
```

## Install with the wizard

Run this command in your project directory to add PostHog to your Go project. The wizard detects your framework, installs the SDK, and sets up your first events:

```bash
npx -y @posthog/wizard@latest
```

Or set it up manually with the steps above.

## Identify users

> **Identifying users is required.** Backend events need a `distinct_id` that matches the ID your frontend uses when calling `posthog.identify()`. Without this, backend events are orphaned. They can't be linked to frontend event captures, [session replays](https://posthog.com/docs/session-replay), [LLM traces](https://posthog.com/docs/ai-engineering), or [error tracking](https://posthog.com/docs/error-tracking).
>
> See the guide on [identifying users](https://posthog.com/docs/getting-started/identify-users) for how to set this up.

## Capture events

You can send custom events using `Capture`:

```go
client.Enqueue(posthog.Capture{
  DistinctId: "distinct_id_of_the_user",
  Event: "user_signed_up",
})
```

> **Tip.** Use an `[object] [verb]` format for your event names, where `[object]` is the entity that the behavior relates to, and `[verb]` is the behavior itself. For example, `project created`, `user signed up`, or `invite sent`.

> **Tip.** You can define event schemas with typed properties and generate type-safe code using [schema management](https://posthog.com/docs/product-analytics/schema-management).

To include more information with the event, add a [properties](https://posthog.com/docs/data/events#event-properties) object:

```go
  client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id_of_the_user",
    Event:      "user_signed_up",
    Properties: posthog.NewProperties().
      Set("login_type", "email").
      Set("is_free_trial", true),
  })
```

## Capture pageviews

If you run PostHog only on your backend and don't capture events from your frontend, you can send `$pageview` events from your backend:

```go
client.Enqueue(posthog.Capture{
  DistinctId: "distinct_id_of_the_user",
  Event:      "$pageview",
  Properties: posthog.NewProperties().
    Set("$current_url", "https://example.com"),
})
```

## Set person properties

For backward compatibility, the Go SDK captures identified events by default. These create [person profiles](https://posthog.com/docs/data/persons). To set [person properties](https://posthog.com/docs/product-analytics/person-properties) in these profiles, include them when capturing an event:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id",
    Event:      "event_name",
    Properties: map[string]interface{}{
        "$set": map[string]interface{}{
            "name": "Max Hedgehog",
        },
        "$set_once": map[string]interface{}{
            "initial_url": "/blog",
        },
    },
})
```

For the difference between `$set` and `$set_once`, see the [person properties docs](https://posthog.com/docs/product-analytics/person-properties#what-is-the-difference-between-set-and-set_once).

To capture [anonymous events](https://posthog.com/docs/data/anonymous-vs-identified-events) without person profiles, set the event's `process_person_profile` option to `false`:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id",
    Event:      "event_name",
    Options:    posthog.NewOptions().Set("process_person_profile", false),
})
```

## Alias users

Sometimes you want to assign multiple distinct IDs to a single user. This helps when your primary distinct ID is inaccessible. For example, a distinct ID used on the frontend isn't available in your backend.

In this case, use `Alias` to assign another distinct ID to the same user:

```go
client.Enqueue(posthog.Alias{
  DistinctId: "distinct_id",
  Alias: "alias_id",
})
```

Read the docs on [alias](https://posthog.com/docs/product-analytics/identify#alias-assigning-multiple-distinct-ids-to-the-same-user) before you use this method.

## Use request context

Use request context to apply a distinct ID, session ID, and common request properties to capture and exception events inside a `net/http` request. This connects frontend activity to backend events, session replay, error tracking, and feature flag evaluation.

If you use [PostHog JS](https://posthog.com/docs/libraries/js) on the frontend, configure [`tracing_headers`](https://posthog.com/docs/libraries/js/config#tracing-headers) for your Go backend hostname, so browser requests include the session and distinct ID headers. Then wrap your handler with `NewRequestContextMiddleware` and use the context-aware helpers:

```go
handler := posthog.NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    flags, err := posthog.EvaluateFlagsWithContext(r.Context(), client, posthog.EvaluateFlagsPayload{})
    if err != nil {
        // If neither the request context nor payload has a distinct ID,
        // err is posthog.ErrNoDistinctID.
    }

    _ = posthog.EnqueueWithContext(r.Context(), client, posthog.Capture{
        Event: "checkout started",
        Flags: flags,
    })
}))
```

The middleware adds `$current_url`, `$request_method`, `$request_path`, `$user_agent`, and `$ip` properties. By default, it also reads `X-PostHog-Distinct-Id` and `X-PostHog-Session-Id` as request-scoped defaults. Explicit `DistinctId` values and `$session_id` properties passed to captures take precedence over request context.

If request context is attached but no distinct ID is available, capture and exception events are sent as [personless events](https://posthog.com/docs/data/anonymous-vs-identified-events) with an auto-generated UUID and the `process_person_profile: false` option. Calls to `Enqueue` without request context still require `DistinctId`.

`EvaluateFlagsWithContext` uses the request-scoped distinct ID when `EvaluateFlagsPayload.DistinctId` is empty. It never generates personless IDs, and it returns `ErrNoDistinctID` when no distinct ID is available.

Tracing headers are client-controlled analytics context, not authentication or authorization. For security-sensitive server-side decisions, pass an authenticated `DistinctId` explicitly or attach one to the request context:

```go
ctx := posthog.WithRequestContext(r.Context(), posthog.RequestContext{
    DistinctId: user.ID,
})
```

To ignore tracing headers while keeping request metadata, disable tracing header capture:

```go
handler := posthog.NewRequestContextMiddleware(
    next,
    posthog.WithCaptureTracingHeaders(false),
)
```

## Evaluate feature flags

PostHog's [feature flags](https://posthog.com/docs/feature-flags) let you safely deploy and roll back new features, and target specific users and groups with them.

Call `client.EvaluateFlags()` once for the user, then read values from the returned snapshot.

For a boolean flag:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "distinct_id_of_your_user",
})
if err != nil {
    // Handle error (e.g. capture error and fallback to default behavior)
}

if flags.IsEnabled("flag-key") {
    // Do something differently for this user
    // Optional: fetch the payload
    matchedFlagPayload := flags.GetFlagPayload("flag-key")
}
```

For a multivariate flag:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "distinct_id_of_your_user",
})
if err != nil {
    // Handle error (e.g. capture error and fallback to default behavior)
}

enabledVariant := flags.GetFlag("flag-key")

if enabledVariant == "variant-key" { // replace "variant-key" with the key of your variant
    // Do something differently for this user
    // Optional: fetch the payload
    matchedFlagPayload := flags.GetFlagPayload("flag-key")
}
```

`flags.GetFlag()` returns the variant string for multivariate flags, `true` for enabled boolean flags, `false` for disabled flags, and `nil` when the flag wasn't returned by the evaluation.

> **Note.** `client.IsFeatureEnabled()`, `client.GetFeatureFlag()`, `client.GetFeatureFlagPayload()`, and `Capture.SendFeatureFlags` still work in posthog-go 1.x.x and 2.x.x, but they're deprecated. Prefer `EvaluateFlags()` for new code.

By default, `EvaluateFlags()` evaluates every flag for the user. If you only need a few flags, pass `FlagKeys` to request only those flags:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "distinct_id_of_your_user",
    FlagKeys:   []string{"checkout-flow", "new-dashboard"},
})
```

## Attach flags to events

To break down or filter events by a feature flag in your [insights](https://posthog.com/docs/product-analytics/insights), include feature flag information in those events. This attributes the flag value to the event correctly.

> **Note.** This step is only required for events captured using the server-side SDKs or the [API](https://posthog.com/docs/api).

There are two ways to do it.

**Pass the evaluated flags snapshot to `Capture`.** Pass the same `flags` object that you used for branching. This attaches the exact flag values from that evaluation and doesn't make another `/flags` request:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "distinct_id_of_your_user",
})
if err != nil {
    // Handle error
}

if flags.IsEnabled("flag-key") {
    // Do something differently for this user
}

client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id_of_your_user",
    Event:      "event_name",
    Flags:      flags,
})
```

By default, this attaches every flag in the snapshot using `$feature/<flag-key>` properties and `$active_feature_flags`.

To reduce event property bloat, pass a filtered snapshot:

```go
// Attach only flags accessed with IsEnabled() or GetFlag() before this call
client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id_of_your_user",
    Event:      "event_name",
    Flags:      flags.OnlyAccessed(),
})

// Attach only specific flags
client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id_of_your_user",
    Event:      "event_name",
    Flags:      flags.Only([]string{"checkout-flow", "new-dashboard"}),
})
```

`OnlyAccessed()` is order-dependent. If you call it before accessing any flags with `IsEnabled()` or `GetFlag()`, no feature flag properties are attached.

**Include the `$feature/feature_flag_name` property manually.** In the event properties, include `$feature/feature_flag_name: variant_key`:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "distinct_id_of_your_user",
    Event:      "event_name",
    Properties: posthog.NewProperties().
        Set("$feature/feature-flag-key", "variant-key"), // replace feature-flag-key with your flag key. Replace "variant-key" with the key of your variant
})
```

## Track flag usage

`$feature_flag_called` events tell PostHog when a user accessed a flag, which powers [analytics and insights](https://posthog.com/docs/product-analytics/insights) on the flag. With `EvaluateFlags()`, the SDK sends this event when you call `flags.IsEnabled()` or `flags.GetFlag()` for a flag.

The SDK deduplicates these events in a local cache. If you reinitialize the PostHog client, the cache resets and `$feature_flag_called` events may be sent again. PostHog handles duplicates, so duplicate `$feature_flag_called` events don't affect your analytics.

`flags.GetFlagPayload()` doesn't send `$feature_flag_called` events and doesn't count as an access for `OnlyAccessed()`.

## Override flag properties

Sometimes you want to evaluate feature flags using [person properties](https://posthog.com/docs/product-analytics/person-properties), [groups](https://posthog.com/docs/product-analytics/group-analytics), or group properties that haven't been ingested yet, or were set incorrectly earlier.

Pass them in the `PersonProperties`, `Groups`, and `GroupProperties` fields. PostHog then uses these values to evaluate the flag, instead of any properties currently stored on your PostHog server:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "distinct_id_of_the_user",
    Groups: posthog.NewGroups().
        Set("your_group_type", "your_group_id").
        Set("another_group_type", "your_group_id"),
    PersonProperties: posthog.NewProperties().
        Set("property_name", "value"),
    GroupProperties: map[string]posthog.Properties{
        "your_group_type": posthog.NewProperties().
            Set("group_property_name", "value"),
        "another_group_type": posthog.NewProperties().
            Set("group_property_name", "value"),
    },
})
if err != nil {
    // Handle error
}

if flags.IsEnabled("flag-key") {
    // Do something differently for this user
}
```

## Override GeoIP properties

By default, a user's GeoIP properties are set using the IP address they use to capture events on the frontend. A common reason to override them is when you don't use PostHog on your frontend, so the user has no GeoIP properties.

To override GeoIP properties, include them in `PersonProperties` alongside your other person properties when you evaluate feature flags. This helps when you evaluate flags on your backend and want to use the client's location instead of your server's location.

You can override these GeoIP properties:

- `$geoip_country_code`
- `$geoip_country_name`
- `$geoip_city_name`
- `$geoip_city_confidence`
- `$geoip_continent_code`
- `$geoip_continent_name`
- `$geoip_latitude`
- `$geoip_longitude`
- `$geoip_postal_code`
- `$geoip_subdivision_1_code`
- `$geoip_subdivision_1_name`
- `$geoip_subdivision_2_code`
- `$geoip_subdivision_2_name`
- `$geoip_subdivision_3_code`
- `$geoip_subdivision_3_name`
- `$geoip_time_zone`

## Set a flag timeout

Set `FeatureFlagRequestTimeout` when you create your PostHog client to set a flag request timeout. This keeps your code from blocking if PostHog's servers are too slow to respond. The default is 3 seconds.

```go
// import "time"

client, _ := posthog.NewWithConfig(
    "<ph_project_token>",
    posthog.Config{
        SecretKey:                 "your secret API key", // Optional, but much more performant. If this key is not supplied, then fetching feature flag values will be slower.
        Endpoint:                  "<ph_client_api_host>",
        FeatureFlagRequestTimeout: 3 * time.Second, // Defaults to 3 seconds.
    },
)
```

## Evaluate flags locally

Evaluating feature flags requires making a request to PostHog for each flag. You can improve performance by evaluating flags locally. Instead of making a request for each flag, PostHog periodically requests and stores feature flag definitions locally, so you can evaluate flags without making additional requests.

Use local evaluation when you can, since it resolves flags faster and with fewer API calls.

Local evaluation needs a `SecretKey` in your client config:

```go
package main

import (
    "os"
    "github.com/posthog/posthog-go"
)

func main() {
	client, _ := posthog.NewWithConfig(
		os.Getenv("POSTHOG_API_KEY"),
		posthog.Config{
			SecretKey: "your secret API key", // Optional, but much more performant. If this key is not supplied, then fetching feature flag values will be slower.
			Endpoint:  "<ph_client_api_host>",
		},
	)
	defer client.Close()
	// run commands
}
```

For how to implement local evaluation, see the [local evaluation guide](https://posthog.com/docs/feature-flags/local-evaluation).

## Run experiments

[Experiments](https://posthog.com/docs/experiments/start-here) use feature flags, so the code for running an experiment is very similar to the feature flags code:

```go
flags, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
    DistinctId: "user_distinct_id",
})
if err != nil {
    // Handle error (e.g. capture error and fallback to default behavior)
}
variant := flags.GetFlag("experiment-feature-flag-key")

if variant == "variant-name" {
    // Do something
}
```

You can also [run experiments without using feature flags](https://posthog.com/docs/experiments/running-experiments-without-feature-flags).

## Capture exceptions

You can capture exceptions and errors with the Go SDK. There are two approaches.

**Direct capture.** `NewDefaultException` generates a stack trace for you:

```go
exception := posthog.NewDefaultException(
    time.Now(),
    "user_distinct_id",
    "DatabaseError",      // type - rendered as title in the UI
    "connection refused",  // value - rendered as description in the UI
)
client.Enqueue(exception)
```

**Automatic capture.** `SlogCaptureHandler` wraps Go's `log/slog` and sends log records at warning level and above as exceptions:

```go
baseHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
})
logger := slog.New(posthog.NewSlogCaptureHandler(baseHandler, client,
    posthog.WithDistinctIDFn(func(ctx context.Context, r slog.Record) string {
        return "user_distinct_id"
    }),
))

// Automatically captured as an exception in PostHog
logger.Warn("Something broke", "error", fmt.Errorf("connection refused"))
```

For the full setup guide, see the [Go error tracking installation docs](https://posthog.com/docs/error-tracking/installation/go).

## Use group analytics

Group analytics lets you associate an event with a group, like a team or an organization. Read the [group analytics](https://posthog.com/docs/product-analytics/group-analytics) guide for more information.

> **Note.** Group analytics is a paid feature. Learn more on the [pricing page](https://posthog.com/pricing).

Send an event associated with a group:

```go
client.Enqueue(posthog.Capture{
    DistinctId: "user_distinct_id",
    Event:      "some_event",
    Groups: posthog.NewGroups().
        Set("company", "company_id_in_your_db"),
})
```

Update properties on a group:

```go
client.Enqueue(posthog.GroupIdentify{
    Type: "company",
    Key:  "company_id_in_your_db",
    Properties: posthog.NewProperties().
        Set("name", "Awesome Inc.").
        Set("employees", 11),
})
```

`name` is a special property. The PostHog UI uses it as the name of the group. If you don't set a `name` property, the group ID is used instead.

## The origin

The Go SDK is largely based on the `analytics-go` package.
