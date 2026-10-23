# PostHog OpenTelemetry bridge for AI observability

`posthogotel` forwards OpenTelemetry AI spans to [PostHog AI observability](https://posthog.com/docs/ai-engineering/observability).

It keeps only spans that follow a known AI semantic convention — a span whose
name or any attribute key starts with `gen_ai.`, `llm.`, `ai.`, or
`traceloop.` — and drops every other span. Kept spans go over OTLP/HTTP to the
PostHog `/i/v0/ai/otel` endpoint with the project API key as a bearer token.

This is a separate Go module, so the core `posthog-go` SDK does not depend on
OpenTelemetry. The module is `github.com/posthog/posthog-go/otel/v2`. It imports
the core SDK for the request context, so its version moves in lockstep with
`posthog-go`, major version included.

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

## Per-request users and properties

```go
import "github.com/posthog/posthog-go/v2"

ctx = posthog.WithRequestContext(ctx, posthog.RequestContext{
    DistinctId: user.ID,
    Properties: posthog.Properties{"plan": "pro"},
})
// Pass this ctx to tracer.Start(ctx, ...) or to the instrumented SDK call, so
// the AI spans started from it are exported with the user and properties.
handleRequest(ctx)
```

An attribute already on the span wins over a property, and a property wins over the context's distinct ID and session ID. The context's session ID is added as `$session_id`, and the context's distinct ID is skipped when the span already has `posthog.distinct_id`, `user.id`, `ai.telemetry.metadata.posthog_distinct_id` or `ai.settings.context.posthog_distinct_id`. To set `$ai_session_id`, add it as a property or span attribute. The processor adds these attributes only to the AI spans it exports to PostHog. Other processors and exporters on the provider don't see them.

`posthog.NewRequestContextMiddleware` sets the request context from the PostHog tracing headers. It also adds request metadata (`$current_url`, `$request_method`, `$request_path`, `$user_agent`, `$ip`), and these are added to the AI spans exported to PostHog. To keep that metadata out, set the request context yourself with `posthog.WithRequestContext` instead of using the middleware.

## Exception trace IDs

Use `posthogotel.EnqueueWithContext` to send a `posthog.Exception` with the active
OpenTelemetry span's `$trace_id` and `$span_id`:

```go
import (
    "github.com/posthog/posthog-go/v2"
    posthogotel "github.com/posthog/posthog-go/otel/v2"
)

err := posthogotel.EnqueueWithContext(ctx, client, posthog.Exception{
    DistinctId: "example-user",
    ExceptionList: []posthog.ExceptionItem{{
        Type: "ExampleError",
        Value: "Example failure",
    }},
})
```

Pass the context from the application operation that failed. The helper reads any
valid active span, including spans that are not sampled. It does not require the
AI span processor or export the span. IDs use lowercase hex with leading zeros.

Event properties override request-context properties, and both override derived
trace IDs. Missing, invalid, or unreadable span context does not prevent exception
capture. The helper copies properties, so reusing an exception does not keep IDs
from a previous span. Other message types use the core enqueue behavior.

This is an explicit integration. `client.Enqueue`, `posthog.EnqueueWithContext`,
and automatic panic capture do not read OpenTelemetry spans. The core SDK does
not depend on OpenTelemetry.

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
