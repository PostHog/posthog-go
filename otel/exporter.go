package posthogotel

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Exporter is a span exporter that keeps only AI spans and forwards them to
// PostHog. Use it when you supply your own span processor, or with a framework
// that accepts only a span exporter. For most setups prefer SpanProcessor.
type Exporter struct {
	// inner is nil when the API key cannot send spans; the Exporter then does
	// nothing.
	inner sdktrace.SpanExporter
}

var _ sdktrace.SpanExporter = (*Exporter)(nil)

// NewExporter builds an Exporter for the given project API key. It returns an
// error only for an invalid option. An empty or personal (phx_) API key logs
// one error and gives an Exporter that exports nothing, so a missing key never
// stops the application.
func NewExporter(ctx context.Context, apiKey string, opts ...Option) (*Exporter, error) {
	cfg, err := newConfig(opts...)
	if err != nil {
		return nil, err
	}
	if unusableAPIKey(apiKey) {
		return &Exporter{}, nil
	}
	inner, err := newOTLPExporter(ctx, apiKey, cfg)
	if err != nil {
		return nil, err
	}
	return &Exporter{inner: inner}, nil
}

// ExportSpans forwards the AI spans in the batch and drops the rest. It returns
// early without a request when the batch has no AI spans.
func (e *Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if e.inner == nil {
		return nil
	}
	aiSpans := filterAISpans(spans)
	if len(aiSpans) == 0 {
		return nil
	}
	for _, span := range aiSpans {
		warnIfPostHogAIGateway(span)
	}
	return e.inner.ExportSpans(ctx, aiSpans)
}

// Shutdown shuts down the underlying OTLP exporter.
func (e *Exporter) Shutdown(ctx context.Context) error {
	if e.inner == nil {
		return nil
	}
	return e.inner.Shutdown(ctx)
}
