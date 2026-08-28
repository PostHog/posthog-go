package posthogotel

import (
	"context"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Exporter is a span exporter that keeps only AI spans and forwards them to
// PostHog. Use it when you supply your own span processor, or with a framework
// that accepts only a span exporter. For most setups prefer SpanProcessor.
type Exporter struct {
	inner sdktrace.SpanExporter
}

var _ sdktrace.SpanExporter = (*Exporter)(nil)

// NewExporter builds an Exporter for the given project API key.
func NewExporter(ctx context.Context, apiKey string, opts ...Option) (*Exporter, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errEmptyAPIKey
	}
	inner, err := newOTLPExporter(ctx, apiKey, newConfig(opts...))
	if err != nil {
		return nil, err
	}
	return &Exporter{inner: inner}, nil
}

// ExportSpans forwards the AI spans in the batch and drops the rest. It returns
// early without a request when the batch has no AI spans.
func (e *Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	aiSpans := filterAISpans(spans)
	if len(aiSpans) == 0 {
		return nil
	}
	return e.inner.ExportSpans(ctx, aiSpans)
}

// Shutdown shuts down the underlying OTLP exporter.
func (e *Exporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}
