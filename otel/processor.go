package posthogotel

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanProcessor is a self-contained span processor that keeps only AI spans,
// batches them, and exports them to PostHog. Register it with
// TracerProvider.RegisterSpanProcessor or the sdktrace.WithSpanProcessor option.
type SpanProcessor struct {
	// inner is nil when the API key cannot send spans; the SpanProcessor then
	// does nothing.
	inner sdktrace.SpanProcessor
}

var _ sdktrace.SpanProcessor = (*SpanProcessor)(nil)

// NewSpanProcessor builds a SpanProcessor for the given project API key. It
// wraps a batch span processor around the PostHog OTLP exporter. It returns an
// error only for an invalid option. An empty or personal (phx_) API key logs
// one error and gives a SpanProcessor that exports nothing, so a missing key
// never stops the application.
func NewSpanProcessor(ctx context.Context, apiKey string, opts ...Option) (*SpanProcessor, error) {
	cfg, err := newConfig(opts...)
	if err != nil {
		return nil, err
	}
	if unusableAPIKey(apiKey) {
		return &SpanProcessor{}, nil
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

// OnStart does no work. Filtering happens in OnEnd, once the span is complete.
func (p *SpanProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd forwards AI spans to the batch processor and drops the rest.
func (p *SpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if p.inner == nil || !IsAISpan(s) {
		return
	}
	warnIfPostHogAIGateway(s)
	p.inner.OnEnd(s)
}

// Shutdown shuts down the underlying batch span processor.
func (p *SpanProcessor) Shutdown(ctx context.Context) error {
	if p.inner == nil {
		return nil
	}
	return p.inner.Shutdown(ctx)
}

// ForceFlush flushes the pending AI spans through the batch span processor.
func (p *SpanProcessor) ForceFlush(ctx context.Context) error {
	if p.inner == nil {
		return nil
	}
	return p.inner.ForceFlush(ctx)
}
