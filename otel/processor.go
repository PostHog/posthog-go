package posthogotel

import (
	"context"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanProcessor is a self-contained span processor that keeps only AI spans,
// batches them, and exports them to PostHog. Register it with
// TracerProvider.RegisterSpanProcessor or the sdktrace.WithSpanProcessor option.
// Spans started from a context carrying posthog.WithRequestContext are exported
// with its distinct ID, session ID and properties. Other processors on the
// provider don't see them.
type SpanProcessor struct {
	inner           sdktrace.SpanProcessor
	requestContexts requestContextStore
}

var _ sdktrace.SpanProcessor = (*SpanProcessor)(nil)

// NewSpanProcessor builds a SpanProcessor for the given project API key. It
// wraps a batch span processor around the PostHog OTLP exporter.
func NewSpanProcessor(ctx context.Context, apiKey string, opts ...Option) (*SpanProcessor, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errEmptyAPIKey
	}
	cfg, err := newConfig(opts...)
	if err != nil {
		return nil, err
	}
	exporter, err := newOTLPExporter(ctx, apiKey, cfg)
	if err != nil {
		return nil, err
	}
	return &SpanProcessor{inner: sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithMaxExportBatchSize(maxSpansPerRequest),
	)}, nil
}

// OnStart captures the PostHog request context carried by ctx (see
// posthog.WithRequestContext) for OnEnd. It never changes the live span.
func (p *SpanProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	if requestContext, ok := capturedRequestContext(ctx); ok {
		p.requestContexts.put(keyOf(s), requestContext)
	}
}

// OnEnd forwards AI spans to the batch processor and drops the rest. A
// forwarded span is a copy carrying the request context captured in OnStart.
func (p *SpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	requestContext, found := p.requestContexts.take(keyOf(s))
	if !IsAISpan(s) {
		return
	}
	warnIfPostHogAIGateway(s)
	if found {
		s = withRequestContext(s, requestContextAttributes(requestContext))
	}
	p.inner.OnEnd(s)
}

// Shutdown shuts down the underlying batch span processor and releases the
// request contexts of spans that never ended.
func (p *SpanProcessor) Shutdown(ctx context.Context) error {
	p.requestContexts.close()
	return p.inner.Shutdown(ctx)
}

// ForceFlush flushes the pending AI spans through the batch span processor.
func (p *SpanProcessor) ForceFlush(ctx context.Context) error {
	return p.inner.ForceFlush(ctx)
}
