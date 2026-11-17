package posthogotel

import (
	"context"

	"github.com/posthog/posthog-go/v2"
	"go.opentelemetry.io/otel/trace"
)

// EnqueueWithContext is the opt-in enqueue path for exceptions with OpenTelemetry
// trace IDs. It adds the derived IDs only when neither the event nor the request
// context sets $trace_id or $span_id. Other messages use
// posthog.EnqueueWithContext without trace enrichment.
func EnqueueWithContext(ctx context.Context, client posthog.EnqueueClient, msg posthog.Message) error {
	switch exception := msg.(type) {
	case posthog.Exception:
		exception.Properties = exceptionTraceProperties(ctx, exception.Properties)
		msg = exception
	case *posthog.Exception:
		if exception != nil {
			copied := *exception
			copied.Properties = exceptionTraceProperties(ctx, exception.Properties)
			msg = &copied
		}
	}
	return posthog.EnqueueWithContext(ctx, client, msg)
}

func exceptionTraceProperties(ctx context.Context, properties posthog.Properties) posthog.Properties {
	derived := activeTraceProperties(ctx)
	if len(derived) == 0 {
		return properties
	}
	requestContext, _ := posthog.RequestContextFromContext(ctx)
	// The derived IDs are a pair, so a caller's trace or span ID replaces both.
	if hasTraceKey(properties) || hasTraceKey(requestContext.Properties) {
		derived = posthog.Properties{}
	}
	// Copied so a client without context support still gets the request's IDs.
	for _, key := range traceKeys {
		if value, exists := requestContext.Properties[key]; exists {
			derived[key] = value
		}
	}
	for key, value := range properties {
		derived[key] = value
	}
	return derived
}

var traceKeys = []string{"$trace_id", "$span_id"}

func hasTraceKey(properties posthog.Properties) bool {
	for _, key := range traceKeys {
		if _, exists := properties[key]; exists {
			return true
		}
	}
	return false
}

func activeTraceProperties(ctx context.Context) (properties posthog.Properties) {
	defer func() { _ = recover() }()
	if ctx == nil {
		return nil
	}
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return nil
	}
	return posthog.Properties{
		"$trace_id": spanContext.TraceID().String(),
		"$span_id":  spanContext.SpanID().String(),
	}
}
