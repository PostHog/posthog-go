# PostHog OpenTelemetry bridge for AI observability

`posthogotel` forwards OpenTelemetry AI spans to [PostHog AI observability](https://posthog.com/docs/ai-engineering/observability).

It keeps only spans that follow a known AI semantic convention — a span whose
name or any attribute key starts with `gen_ai.`, `llm.`, `ai.`, or
`traceloop.` — and drops every other span. Kept spans go over OTLP/HTTP to the
PostHog `/i/v0/ai/otel` endpoint with the project API key as a bearer token.

This is a separate Go module, so the core `posthog-go` SDK does not depend on
OpenTelemetry.

## Usage

`SpanProcessor` is the recommended integration. Register it on the
`TracerProvider` your application already owns — rather than replacing the
global provider with a new one — so your resource, sampler, and existing
exporters are kept and tracers already handed out (such as ADK Go's) route
through it. Shut down the processor with a fresh context to flush its buffered
spans without shutting down the application-owned provider.

If you don't already have a `TracerProvider`, create one and register the
processor on it, as [`example/`](example) does. Use `WithHost` for a host other
than PostHog US cloud. For a framework that accepts only a span exporter, use
`NewExporter` instead and pair it with your own batch span processor.

## Failed generations

PostHog decides that a generation failed from the OpenTelemetry span status, so set the
status to the error code when a model call fails. Recording the error on the span is not
enough on its own: in OpenTelemetry for Go that only adds an exception event and leaves
the span status unset, so the failed generation reaches PostHog looking successful with an
empty response. The Python and JavaScript instrumentation sets the status for you, which
is why this step is specific to Go. Once the status is set, PostHog fills in the error
message and HTTP status from the recorded exception event.

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
