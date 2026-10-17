package posthogotel

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/posthog/posthog-go"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ContextSpanProcessor adds PostHog IDs to spans from the application's provider.
// Register it before processors that consume attributes in OnStart. It does not export spans.
type ContextSpanProcessor struct{}

var _ sdktrace.SpanProcessor = (*ContextSpanProcessor)(nil)

// NewContextSpanProcessor creates an optional bridge for general application spans.
// It does not require an API key or change the application's tracing configuration.
func NewContextSpanProcessor() *ContextSpanProcessor { return &ContextSpanProcessor{} }

func (*ContextSpanProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	enrichContextSpan(ctx, span)
}

func (*ContextSpanProcessor) OnEnd(sdktrace.ReadOnlySpan)      {}
func (*ContextSpanProcessor) Shutdown(context.Context) error   { return nil }
func (*ContextSpanProcessor) ForceFlush(context.Context) error { return nil }

type contextSpanRequestKey struct{}
type contextSpanRequest struct{ active atomic.Bool }

func enrichContextSpan(ctx context.Context, span trace.Span) {
	if ctx == nil || span == nil || !span.IsRecording() {
		return
	}
	if request, ok := ctx.Value(contextSpanRequestKey{}).(*contextSpanRequest); ok && !request.active.Load() {
		return
	}
	readable, ok := span.(interface{ Attributes() []attribute.KeyValue })
	if !ok {
		return
	}
	request, ok := posthog.RequestContextFromContext(ctx)
	if !ok {
		return
	}
	hasDistinctID, hasSessionID := false, false
	for _, attr := range readable.Attributes() {
		switch string(attr.Key) {
		case "posthogDistinctId":
			hasDistinctID = true
		case "sessionId":
			hasSessionID = true
		}
	}
	attrs := make([]attribute.KeyValue, 0, 2)
	if request.DistinctId != "" && !hasDistinctID {
		attrs = append(attrs, attribute.String("posthogDistinctId", request.DistinctId))
	}
	if request.SessionId != "" && !hasSessionID {
		attrs = append(attrs, attribute.String("sessionId", request.SessionId))
	}
	span.SetAttributes(attrs...)
}

// NewRequestContextMiddleware also enriches the existing request span after header extraction.
// It accepts the core middleware options. Spans started from an inherited request context
// after the handler returns omit request IDs. Register ContextSpanProcessor for child spans.
func NewRequestContextMiddleware(next http.Handler, opts ...posthog.RequestContextOption) http.Handler {
	if next == nil {
		return posthog.NewRequestContextMiddleware(nil, opts...)
	}
	return posthog.NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := &contextSpanRequest{}
		request.active.Store(true)
		defer request.active.Store(false)
		ctx := context.WithValue(r.Context(), contextSpanRequestKey{}, request)
		enrichContextSpan(ctx, trace.SpanFromContext(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	}), opts...)
}
