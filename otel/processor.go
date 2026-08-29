package posthogotel

import (
	"context"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanProcessor is a self-contained span processor that keeps only AI spans,
// batches them, and exports them to PostHog. Register it with
// TracerProvider.RegisterSpanProcessor or the sdktrace.WithSpanProcessor option.
type SpanProcessor struct {
	inner sdktrace.SpanProcessor
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

// OnStart does no work. Filtering happens in OnEnd, once the span is complete.
func (p *SpanProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd forwards AI spans to the batch processor and drops the rest.
func (p *SpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if !IsAISpan(s) {
		return
	}
	warnIfPostHogAIGateway(s)
	p.inner.OnEnd(s)
}

// Shutdown shuts down the underlying batch span processor.
func (p *SpanProcessor) Shutdown(ctx context.Context) error {
	return p.inner.Shutdown(ctx)
}

// ForceFlush flushes the pending AI spans through the batch span processor.
func (p *SpanProcessor) ForceFlush(ctx context.Context) error {
	return p.inner.ForceFlush(ctx)
}
