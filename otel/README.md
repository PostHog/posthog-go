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

// Flush buffered spans on shutdown. Use a fresh context, not one that is
// already canceled (for example from signal.NotifyContext): the SDK skips
// the final export on a canceled context and silently drops queued spans.
defer func() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown tracer provider: %v", err)
	}
}()
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

Those spans carry the generation's model, token counts, latency, and finish
reason. They do **not** carry prompt and response message content: ADK Go emits
message bodies as OpenTelemetry **log records** (event names
`gen_ai.system.message`, `gen_ai.user.message`, and `gen_ai.choice`), gated
behind the `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` environment
variable, not as span attributes. This bridge forwards spans only, so prompt and
response fields stay empty for ADK Go generations.

See [`example/`](example) for a runnable program.
