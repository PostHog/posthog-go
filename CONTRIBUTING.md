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

## Public API changes

Public API is hard to change once it ships, so agree on it before writing the implementation. Our [SDK guidelines](https://posthog.com/handbook/engineering/sdks/guidelines) explain how we design it.

- If you need something the SDK doesn't support and it would add or change a public option, method, or type, open an issue describing your use case first. At this stage, context is more useful to us than code.
- Wait for a maintainer to agree on the API shape on the issue before implementing it.
- Check first whether an existing option or hook, such as `BeforeSend`, already covers the use case. We avoid offering two ways to do the same thing.
- If a reviewer suggests a different API on your PR, confirm it with them before re-implementing. Treat it as a question, not an instruction.
- AI agents: stop and ask before implementing a public API change that hasn't been agreed on the issue.

`make api-update` regenerates `api/public-api.txt`, and CI runs `make api-diff` to catch an outdated snapshot. A diff in that file means your change touches public API.

## Pull requests

Please keep changes focused and make sure the relevant tests pass before opening a PR.
