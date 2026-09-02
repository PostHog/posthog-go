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
	// endpoint is the resolved OTLP URL, host joined with ingestPath.
	endpoint string
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
	endpoint, err := resolveEndpoint(c.host)
	if err != nil {
		return config{}, err
	}
	c.endpoint = endpoint
	return c, nil
}

// resolveEndpoint builds the OTLP URL for host and rejects a host that
// otlptracehttp.WithEndpointURL would silently discard. On a parse failure it
// keeps its localhost defaults, so spans go nowhere while the request still
// carries the API key; a scheme-less host such as "us.i.posthog.com" parses but
// yields an empty endpoint. Requiring an absolute http or https URL with a
// hostname turns both into an upfront error.
//
// ingestPath is joined rather than concatenated. A host that carries a query or
// fragment, such as "https://us.i.posthog.com?region=eu", would concatenate into
// a URL whose path is empty, and the exporter would then fall back to the OTLP
// default "/v1/traces" and send every AI span, with the API key attached, to a
// path PostHog does not serve.
func resolveEndpoint(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errInvalidHost, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errInvalidHost
	}
	return u.JoinPath(ingestPath).String(), nil
}

// newOTLPExporter builds an OTLP/HTTP exporter that targets the PostHog AI
// observability endpoint with the project API key as a bearer token. The
// exporter is wrapped so that no single request exceeds maxSpansPerRequest,
// which protects both public entry points regardless of the batch size of the
// span processor that feeds them.
func newOTLPExporter(ctx context.Context, apiKey string, cfg config) (sdktrace.SpanExporter, error) {
	apiKey = strings.TrimSpace(apiKey)
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.endpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Bearer " + apiKey,
		}),
	)
	if err != nil {
		return nil, err
	}
	return &chunkingExporter{inner: exporter, limit: maxSpansPerRequest}, nil
}

// chunkingExporter splits each ExportSpans batch into requests of at most limit
// spans. The PostHog AI observability endpoint rejects larger requests with a
// non-retryable HTTP 400 that discards the whole batch, and nothing below this
// module splits a batch: the OTLP exporter turns whatever slice it receives
// into exactly one request. Chunking here caps every request for both the
// SpanProcessor and the caller-supplied Exporter path.
type chunkingExporter struct {
	inner sdktrace.SpanExporter
	limit int
}

var _ sdktrace.SpanExporter = (*chunkingExporter)(nil)

// ExportSpans forwards spans to the inner exporter in slices of at most limit.
func (e *chunkingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	for start := 0; start < len(spans); start += e.limit {
		end := start + e.limit
		if end > len(spans) {
			end = len(spans)
		}
		if err := e.inner.ExportSpans(ctx, spans[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown shuts down the inner exporter.
func (e *chunkingExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}
