# PostHog Go

[![Go Reference](https://pkg.go.dev/badge/github.com/posthog/posthog-go.svg)](https://pkg.go.dev/github.com/posthog/posthog-go)
![min. Go Version](https://img.shields.io/github/go-mod/go-version/PostHog/posthog-go?label=min.%20Go%20version%20)

Please see the main [PostHog docs](https://posthog.com/docs).

SDK usage examples and code snippets live in the official documentation so they stay up to date.

## Documentation

- [Go library docs](https://posthog.com/docs/libraries/go)

## AI observability

The [`otel`](otel) module is an OpenTelemetry bridge that forwards AI spans
(`gen_ai.*`, `llm.*`, and similar) to PostHog AI observability, including spans
from a Google Agent Development Kit (ADK) for Go agent. It is a separate Go
module, so the core SDK stays free of OpenTelemetry dependencies.

## Questions?

### [Visit the community forum.](https://posthog.com/questions)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local setup and test instructions.

## Releasing

See [RELEASING.md](RELEASING.md).
