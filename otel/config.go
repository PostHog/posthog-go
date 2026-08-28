package posthogotel

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// DefaultHost is the PostHog US cloud host used when WithHost is not set.
	DefaultHost = "https://us.i.posthog.com"

	// ingestPath is the PostHog AI observability OTLP endpoint path.
	ingestPath = "/i/v0/ai/otel"
)

// errEmptyAPIKey is returned when the project API key is missing.
var errEmptyAPIKey = errors.New("posthogotel: apiKey must not be empty")

// config holds the resolved settings for the exporter and the span processor.
type config struct {
	host string
}

// Option configures the exporter or the span processor.
type Option func(*config)

// WithHost sets the PostHog host, for example "https://eu.i.posthog.com".
// An empty or blank value keeps DefaultHost.
func WithHost(host string) Option {
	return func(c *config) {
		if h := strings.TrimSpace(host); h != "" {
			c.host = h
		}
	}
}

func newConfig(opts ...Option) config {
	c := config{host: DefaultHost}
	for _, opt := range opts {
		opt(&c)
	}
	c.host = strings.TrimRight(c.host, "/")
	return c
}

// newOTLPExporter builds an OTLP/HTTP exporter that targets the PostHog AI
// observability endpoint with the project API key as a bearer token.
func newOTLPExporter(ctx context.Context, apiKey string, cfg config) (sdktrace.SpanExporter, error) {
	return otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.host+ingestPath),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Bearer " + apiKey,
		}),
	)
}
