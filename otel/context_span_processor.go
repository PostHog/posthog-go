package posthogotel

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/posthog/posthog-go/v2"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ContextSpanProcessor adds PostHog IDs to spans from the application's provider.
// Register it before processors that consume attributes in OnStart. It does not export spans.
type ContextSpanProcessor struct {
	client   contextClient
	shutdown atomic.Bool
}

var _ sdktrace.SpanProcessor = (*ContextSpanProcessor)(nil)

// NewContextSpanProcessor creates an optional bridge for general application spans.
// It adds IDs only while client is enabled, so a nil, disabled, or closed client adds none.
// A client wrapper can implement IsEnabled() bool to report its state; otherwise it counts as enabled.
func NewContextSpanProcessor(client posthog.Client) *ContextSpanProcessor {
	return &ContextSpanProcessor{client: newContextClient(client)}
}

func (p *ContextSpanProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	if !p.shutdown.Load() {
		enrichContextSpan(ctx, span, p.client)
	}
}

func (*ContextSpanProcessor) OnEnd(sdktrace.ReadOnlySpan) {}
func (p *ContextSpanProcessor) Shutdown(context.Context) error {
	p.shutdown.Store(true)
	return nil
}
func (*ContextSpanProcessor) ForceFlush(context.Context) error { return nil }

type contextClient struct {
	present bool
	enabled interface{ IsEnabled() bool }
}

func newContextClient(client posthog.Client) contextClient {
	enabled, _ := client.(interface{ IsEnabled() bool })
	return contextClient{present: client != nil, enabled: enabled}
}

func (c contextClient) isEnabled() bool {
	return c.present && (c.enabled == nil || c.enabled.IsEnabled())
}

type contextSpanRequestKey struct{}
type contextSpanRequest struct{ active atomic.Bool }

func enrichContextSpan(ctx context.Context, span trace.Span, client contextClient) {
	if !client.present || ctx == nil || span == nil || !span.IsRecording() {
		return
	}
	if request, ok := ctx.Value(contextSpanRequestKey{}).(*contextSpanRequest); ok && !request.active.Load() {
		return
	}
	if !client.isEnabled() {
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
// It adds IDs to spans only while client is enabled, like NewContextSpanProcessor.
// It accepts the core middleware options. Spans started from an inherited request context
// after the handler returns omit request IDs. Register ContextSpanProcessor for child spans.
func NewRequestContextMiddleware(client posthog.Client, next http.Handler, opts ...posthog.RequestContextOption) http.Handler {
	spanClient := newContextClient(client)
	if next == nil {
		return posthog.NewRequestContextMiddleware(nil, opts...)
	}
	return posthog.NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := &contextSpanRequest{}
		request.active.Store(true)
		defer request.active.Store(false)
		ctx := context.WithValue(r.Context(), contextSpanRequestKey{}, request)
		enrichContextSpan(ctx, trace.SpanFromContext(ctx), spanClient)
		next.ServeHTTP(w, r.WithContext(ctx))
	}), opts...)
}
