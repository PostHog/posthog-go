# PostHog Go

[![Go Reference](https://pkg.go.dev/badge/github.com/posthog/posthog-go/v2.svg)](https://pkg.go.dev/github.com/posthog/posthog-go/v2)
![min. Go Version](https://img.shields.io/github/go-mod/go-version/PostHog/posthog-go?label=min.%20Go%20version%20)

Please see the main [PostHog docs](https://posthog.com/docs).

SDK usage examples and code snippets live in the official documentation so they stay up to date.

## Documentation

- [Go library docs](https://posthog.com/docs/libraries/go)

Upgrading from posthog-go 1.x.x? Read the [posthog-go 2.0.0 migration guide](docs/migration-v2.md).

## AI observability

The [`otel`](otel) module is an OpenTelemetry bridge that forwards AI spans
(`gen_ai.*`, `llm.*`, and similar) to PostHog AI observability, including spans
from a Google Agent Development Kit (ADK) for Go agent. It is a separate Go
module, so the core SDK stays free of OpenTelemetry dependencies.

Use the [evaluations package](ai/evaluations) to manage AI datasets and scorer
versions, pin a dataset snapshot, and upload results to an offline experiment.
It uses a project ID and secret key and acknowledges each operation directly.
Use HTTPS for remote hosts. An explicit HTTP host sends the secret key in cleartext.
Set `Config.SecretKey` to a project secret API key for that project or a personal API key.

| Operation | Required scopes |
| --- | --- |
| Read or manage datasets | `dataset:read` / `dataset:write` |
| Read or manage scorers | `llm_analytics:read` / `llm_analytics:write` |
| Create, upload, complete, or fail an experiment | `offline_evaluation_ingestion:write` |
| Read or replace experiment tags | `evaluation:read` / `evaluation:write` |
| Read item or result revisions / correct accepted data | `evaluation:read` / `evaluation:write` |

Reading a result revision, changing a result or its payload, and deleting an item also require
`llm_analytics:read`. To calculate composites from stored inputs, grant ingestion plus
`evaluation:read` and `llm_analytics:read`. An ingestion-only key must submit every pinned input
in the same upload. Personal keys also need the user's resource permissions, including evaluation
editor access for corrections. Dataset exports require a personal API key with `dataset:read`.

Create a composite scorer with `Scope: "experiment"` to compute a score for a completed run.
Its formula inputs reference item scorer versions and select statistics such as `pass_rate` or
`mean`. Set `ApplyToSuiteKeys` to choose defaults for future runs. Use `suite_key` to select
the default or pass `experiment_scorer_version_id` to pin a specific version at creation.

See the [Go example](ai/evaluations/example_test.go) for a dataset-backed run
and the [integration test](ai/evaluations/docs_integration_test.go) for the
support-agent workflow from the offline evals quickstart.

## Questions?

### [Visit the community forum.](https://posthog.com/questions)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local setup and test instructions.

## Releasing

See [RELEASING.md](RELEASING.md).
