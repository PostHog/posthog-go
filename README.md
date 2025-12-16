# PostHog Go

[![Go Reference](https://pkg.go.dev/badge/github.com/posthog/posthog-go.svg)](https://pkg.go.dev/github.com/posthog/posthog-go)
![min. Go Version](https://img.shields.io/github/go-mod/go-version/PostHog/posthog-go?label=min.%20Go%20version%20)

Please see the main [PostHog docs](https://posthog.com/docs).

Specifically, the [Go integration](https://posthog.com/docs/integrations/go-integration) details.

## Quickstart

Install posthog to your gopath

```bash
$ go get github.com/posthog/posthog-go
```

Go 🦔!

```go
package main

import (
    "os"
    "github.com/posthog/posthog-go"
)

func main() {
    client := posthog.New(os.Getenv("POSTHOG_API_KEY")) // This value must be set to the project API key in PostHog
    // alternatively, you can do
    // client, _ := posthog.NewWithConfig(
    //     os.Getenv("POSTHOG_API_KEY"),
    //     posthog.Config{
    //         PersonalApiKey: "your personal API key", // Set this to your personal API token you want feature flag evaluation to be more performant.  This will incur more costs, though
    //         Endpoint:       "https://us.i.posthog.com",
    //     },
    // )
    defer client.Close()

    // Capture an event
    client.Enqueue(posthog.Capture{
      DistinctId: "test-user",
      Event:      "test-snippet",
      Properties: posthog.NewProperties().
        Set("plan", "Enterprise").
        Set("friends", 42),
    })

    // Add context for a user
    client.Enqueue(posthog.Identify{
      DistinctId: "user:123",
      Properties: posthog.NewProperties().
        Set("email", "john@doe.com").
        Set("proUser", false),
    })

    // Link user contexts
    client.Enqueue(posthog.Alias{
      DistinctId: "user:123",
      Alias: "user:12345",
    })

    // Capture a pageview
    client.Enqueue(posthog.Capture{
      DistinctId: "test-user",
      Event:      "$pageview",
      Properties: posthog.NewProperties().
        Set("$current_url", "https://example.com"),
    })

    // Capture an error / exception
    client.Enqueue(posthog.NewDefaultException(
      time.Now(),
      "distinct-id",
      "Error title",
      "Error Description",
    ))

    // Create a logger which automatically captures warning logs and above
    baseLogHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
    logger := slog.New(posthog.NewSlogCaptureHandler(baseLogHandler, client,
      posthog.WithMinCaptureLevel(slog.LevelWarn),
      posthog.WithDistinctIDFn(func(ctx context.Context, r slog.Record) string {
        // for demo purposes, real applications should likely pull this value from the context.
        return "my-user-id"
      }),
    })
    logger.Warn("Log that something broke", "error", fmt.Errorf("this is a dummy scenario"))

    // Capture event with calculated uuid to deduplicate repeated events.
    // The library github.com/google/uuid is used
    key := myEvent.Id + myEvent.Project
    uid := uuid.NewSHA1(uuid.NameSpaceX500, []byte(key)).String()
    client.Enqueue(posthog.Capture{
      Uuid: uid,
      DistinctId: "test-user",
      Event:      "$pageview",
      Properties: posthog.NewProperties().
        Set("$current_url", "https://example.com"),
    })

    // Check if a feature flag is enabled
    isMyFlagEnabled, err := client.IsFeatureEnabled(
            FeatureFlagPayload{
                Key:        "flag-key",
                DistinctId: "distinct_id_of_your_user",
            })

    if isMyFlagEnabled == true {
        // Do something differently for this user
    }
}
```

## Development

Make sure you have Go installed (macOS: `brew install go`, Linx / Windows: https://go.dev/doc/install).

To build the project:

```bash
# Install dependencies
make dependencies

# Run tests and build
make build

# Just run tests
make test
```

## Testing Locally

You can run your Go app against a local build of `posthog-go` by making the following change to your `go.mod` file for whichever your app, e.g.

```Go
module example/posthog-go-app

go 1.22.5

require github.com/posthog/posthog-go v0.0.0-20240327112532-87b23fe11103

require github.com/google/uuid v1.3.0 // indirect

replace github.com/posthog/posthog-go => /path-to-your-local/posthog-go
```

## Examples

Check out the [examples](examples/README.md) for more detailed examples of how to use the PostHog Go client.

## Running the examples

The examples demonstrate different features of the PostHog Go client. To run all examples:

### Option 1: Using .env file (Recommended)

```bash
# Copy the example .env file and fill in your credentials
cd examples
cp .env.example .env
# Edit .env with your actual API keys

# Run all examples
go run *.go
```

### Option 2: Using environment variables

```bash
# Set your PostHog API keys and endpoint (optional)
export POSTHOG_PROJECT_API_KEY="your-project-api-key"
export POSTHOG_PERSONAL_API_KEY="your-personal-api-key"
export POSTHOG_ENDPOINT="https://app.posthog.com"  # Optional, defaults to http://localhost:8000

# Run all examples
go run examples/*.go
```

This will run:

- Feature flags example
- Capture events example
- Capture events with feature flag options example

### Prerequisites

Before running the examples, you'll need to:

1. Have a PostHog instance running (default: http://localhost:8000)

   - You can modify the endpoint by setting the `POSTHOG_ENDPOINT` environment variable
   - If not set, it defaults to "http://localhost:8000"

2. Set up the following feature flags in your PostHog instance:

   - `multivariate-test` (a multivariate flag)
   - `simple-test` (a simple boolean flag)
   - `multivariate-simple-test` (a multivariate flag)
   - `my_secret_flag_value` (a remote config flag with string payload)
   - `my_secret_flag_json_object_value` (a remote config flag with JSON object payload)
   - `my_secret_flag_json_array_value` (a remote config flag with JSON array payload)

3. Set your PostHog API keys as environment variables:
   - `POSTHOG_PROJECT_API_KEY`: Your project API key (starts with `phc_...`)
   - `POSTHOG_PERSONAL_API_KEY`: Your personal API key (starts with `phx_...`)

## Releasing

Before creating a release make sure you have installed [`gh`](https://cli.github.com) and authenticated via `gh auth login`

To release a new version of the PostHog Go client, follow these steps:

1. Update the version in the `version.go` file
2. Update the changelog in `CHANGELOG.md`
3. Once your changes are merged into main, create a new tag and release with the new version

```bash
git tag v1.4.7
git push --tags
gh release create v1.4.7 --generate-notes
```

Releases are installed directly from GitHub.

## Event Delivery and Retry Behavior

The PostHog Go client includes automatic retry logic for handling transient network failures. Understanding when events are delivered vs dropped helps ensure reliable analytics.

### Events Are Delivered (Not Dropped)

The client automatically retries on network errors and will successfully deliver events when:

- **Transient network failures** - EOF errors, connection resets, TCP drops that recover within retry attempts
- **Server temporarily unavailable** - If the server starts responding before max retries are exhausted
- **Connection drops at any stage** - Whether after connect, during headers, or while sending body

Example scenarios that recover successfully:
- Server closes connection without response (EOF) but succeeds on retry
- TCP connection dropped after partial body read
- Temporary network interruption lasting a few seconds

### Events Are Dropped

Events will be permanently lost in these scenarios:

| Scenario | Behavior |
|----------|----------|
| **Max retries exceeded** | After 10 failed attempts, events are dropped and `Failure` callback is invoked |
| **Client closed during retry** | If `client.Close()` is called while retrying, pending events are dropped |
| **Non-retryable errors** | JSON marshalling failures cause immediate drop (no retry) |
| **HTTP 4xx responses** | Client errors (e.g., invalid API key) are not retried |

### Configuring Retry Behavior

You can customize retry timing via the `RetryAfter` config option:

```go
client, _ := posthog.NewWithConfig(
    "api-key",
    posthog.Config{
        RetryAfter: func(attempt int) time.Duration {
            // Custom backoff: 100ms, 200ms, 400ms, ...
            return time.Duration(100<<attempt) * time.Millisecond
        },
    },
)
```

To limit the number of retries (default is 9 retries = 10 total attempts):

```go
client, _ := posthog.NewWithConfig(
    "api-key",
    posthog.Config{
        MaxRetries: posthog.Ptr(3), // 3 retries = 4 total attempts
    },
)
```

Setting `MaxRetries` to 0 means only one attempt with no retries (disable retries):

```go
posthog.Config{
    MaxRetries: posthog.Ptr(0), // 3 retries = 4 total attempts
},
```

### Monitoring Event Delivery

Use the `Callback` interface to track successes and failures:

```go
type MyCallback struct{}

func (c *MyCallback) Success(msg posthog.APIMessage) {
    log.Printf("Event delivered: %v", msg)
}

func (c *MyCallback) Failure(msg posthog.APIMessage, err error) {
    log.Printf("Event dropped: %v, error: %v", msg, err)
    // Optionally: persist to disk, send to dead-letter queue, etc.
}

client, _ := posthog.NewWithConfig("api-key", posthog.Config{
    Callback: &MyCallback{},
})
```

## Questions?

### [Visit the community forum.](https://posthog.com/questions)
