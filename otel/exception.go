package posthogotel

import (
	"context"

	"github.com/posthog/posthog-go"
	"go.opentelemetry.io/otel/trace"
)

// EnqueueWithContext is the opt-in enqueue path for exceptions with OpenTelemetry
// trace IDs. It keeps event and request-context properties ahead of derived IDs.
// Other messages use posthog.EnqueueWithContext without trace enrichment.
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
	for key := range derived {
		if _, exists := requestContext.Properties[key]; exists {
			delete(derived, key)
		}
	}
	for key, value := range properties {
		derived[key] = value
	}
	return derived
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
