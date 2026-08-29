package posthogotel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// DefaultHost is the PostHog US cloud host used when WithHost is not set.
	DefaultHost = "https://us.i.posthog.com"

	// ingestPath is the PostHog AI observability OTLP endpoint path.
	ingestPath = "/i/v0/ai/otel"

	// maxSpansPerRequest is the maximum number of AI spans the PostHog AI
	// observability endpoint accepts in a single OTLP request. Larger requests
	// are rejected with a non-retryable HTTP 400 and the whole batch is lost, so
	// batches must be capped at this limit.
	maxSpansPerRequest = 100
)

// errEmptyAPIKey is returned when the project API key is missing.
var errEmptyAPIKey = errors.New("posthogotel: apiKey must not be empty")

// errInvalidHost is returned when the configured host is not an absolute http
// or https URL with a hostname.
var errInvalidHost = errors.New("posthogotel: host must be an absolute http or https URL, for example https://us.i.posthog.com")

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

func newConfig(opts ...Option) (config, error) {
	c := config{host: DefaultHost}
	for _, opt := range opts {
		opt(&c)
	}
	c.host = strings.TrimRight(c.host, "/")
	if err := validateHost(c.host); err != nil {
		return config{}, err
	}
	return c, nil
}

// validateHost rejects a host that otlptracehttp.WithEndpointURL would silently
// discard. On a parse failure it keeps its localhost defaults, so spans go
// nowhere while the request still carries the API key; a scheme-less host such
// as "us.i.posthog.com" parses but yields an empty endpoint. Requiring an
// absolute http or https URL with a hostname turns both into an upfront error.
func validateHost(host string) error {
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidHost, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errInvalidHost
	}
	return nil
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
