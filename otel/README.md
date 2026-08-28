# PostHog OpenTelemetry bridge for AI observability

`posthogotel` forwards OpenTelemetry AI spans to [PostHog AI observability](https://posthog.com/docs/ai-engineering/observability).

It keeps only spans that follow a known AI semantic convention — a span whose
name or any attribute key starts with `gen_ai.`, `llm.`, `ai.`, or
`traceloop.` — and drops every other span. Kept spans go over OTLP/HTTP to the
PostHog `/i/v0/ai/otel` endpoint with the project API key as a bearer token.

This is a separate Go module, so the core `posthog-go` SDK does not depend on
OpenTelemetry. Add it on its own:

```bash
go get github.com/posthog/posthog-go/otel
```

## Usage

`SpanProcessor` is the recommended integration. Register it on your
`TracerProvider`:

```go
processor, err := posthogotel.NewSpanProcessor(ctx, "phc_your_project_api_key")
if err != nil {
	log.Fatal(err)
}
provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
otel.SetTracerProvider(provider)
defer provider.Shutdown(ctx)
```

Use `WithHost` for a host other than PostHog US cloud, for example
`posthogotel.WithHost("https://eu.i.posthog.com")`.

For a framework that accepts only a span exporter, use `NewExporter` instead and
pair it with your own batch span processor.

## Google Agent Development Kit (ADK) for Go

[ADK Go](https://google.golang.org/adk) instruments its agents with
OpenTelemetry and emits `gen_ai.*` spans on the global tracer provider. Register
the PostHog span processor on that provider before you run the agent, and the
agent's `gen_ai.*` spans reach PostHog with no further code.

See [`example/`](example) for a runnable program.
