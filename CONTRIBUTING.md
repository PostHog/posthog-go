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

This section is for external contributors. PostHog maintainers (members of the PostHog GitHub org) agree on API shape in the PR itself, so they don't need a separate issue.

- **Before you start:** if you need something the SDK doesn't support and it would add or change a public option, method, or type, open an issue describing your use case. Wait for a maintainer to agree on the API shape there before you implement it. Context is more useful to us than code at this stage.
- **Already specified?** If a published [sdk-spec](https://github.com/PostHog/sdk-specs) defines the API, that's the agreement, so you don't need an issue.
- **Already have a PR open?** Don't stop or rewrite it. Call out the public API change at the top of the PR description, and link or open an issue so we can discuss the shape there.
- Check first whether an existing option or hook, such as `BeforeSend`, already covers the use case. We avoid offering two ways to do the same thing.
- If a reviewer suggests a different API on your PR, confirm it with them before re-implementing. Treat it as a question, not an instruction.

`make api-update` regenerates `api/public-api.txt`, and CI runs `make api-diff` to catch an outdated snapshot. A diff in that file means your change touches public API.

## Pull requests

Please keep changes focused and make sure the relevant tests pass before opening a PR.
