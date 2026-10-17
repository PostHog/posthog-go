// Package posthogotel forwards OpenTelemetry AI spans to PostHog AI observability.
//
// It keeps only spans that follow a known AI semantic convention. A span
// qualifies when its name or any of its attribute keys starts with one of
// "gen_ai.", "llm.", "ai.", or "traceloop.". Every other span is dropped.
// Kept spans go over OTLP/HTTP to the PostHog "/i/v0/ai/otel" endpoint with the
// project API key in an Authorization: Bearer header.
//
// The package offers two integrations:
//
//   - SpanProcessor is the recommended integration. It filters spans, batches
//     the AI spans, and exports them. Register it with
//     TracerProvider.RegisterSpanProcessor (or the WithSpanProcessor option).
//     It also adds the PostHog request context (see posthog.WithRequestContext)
//     to the AI spans it exports, which Exporter can't.
//
//   - Exporter is for setups that supply their own span processor, or
//     frameworks that accept only a span exporter. It filters spans and
//     delegates the AI spans to an OTLP/HTTP exporter.
//
// ContextSpanProcessor adds IDs to general application spans without exporting
// them. NewRequestContextMiddleware also enriches the existing incoming span and
// limits request IDs to the handler lifetime. These integrations use the
// application's provider and exporter.
//
// This is a separate Go module so that the core posthog-go SDK does not depend
// on OpenTelemetry. This module depends on the core module for the request
// context.
package posthogotel
