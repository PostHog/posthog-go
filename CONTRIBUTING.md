# Contributing

Thanks for your interest in improving the PostHog Go SDK.

## Development setup

Make sure you have Go installed (macOS: `brew install go`, Linux / Windows: https://go.dev/doc/install).

From the repository root:

```bash
# Install dependencies
make dependencies

# Run tests and build
make build

# Just run tests
make test
```

## Testing local changes in another app

You can run your Go app against a local build of `posthog-go` by updating your app's `go.mod`, for example:

```go
module example/posthog-go-app

go 1.22.5

require github.com/posthog/posthog-go v0.0.0-20240327112532-87b23fe11103

require github.com/google/uuid v1.3.0 // indirect

replace github.com/posthog/posthog-go => /path-to-your-local/posthog-go
```

## Pull requests

Please keep changes focused and make sure the relevant tests pass before opening a PR.
