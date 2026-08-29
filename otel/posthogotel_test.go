package posthogotel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// recordSpan starts and ends a span with the given name and attribute keys,
// then returns the resulting ReadOnlySpan.
func recordSpan(t *testing.T, name string, attrKeys ...string) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	attrs := make([]attribute.KeyValue, len(attrKeys))
	for i, key := range attrKeys {
		attrs[i] = attribute.String(key, "value")
	}
	_, span := provider.Tracer("test").Start(context.Background(), name)
	span.SetAttributes(attrs...)
	span.End()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 recorded span, got %d", len(ended))
	}
	return ended[0]
}

func TestIsAISpan(t *testing.T) {
	cases := []struct {
		name     string
		spanName string
		attrKeys []string
		want     bool
	}{
		{"name gen_ai prefix", "gen_ai.chat", nil, true},
		{"name llm prefix", "llm.request", nil, true},
		{"name ai prefix", "ai.completion", nil, true},
		{"name traceloop prefix", "traceloop.workflow", nil, true},
		{"attribute key prefix", "handler", []string{"gen_ai.system"}, true},
		{"non-ai name and attributes", "http.request", []string{"http.method"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span := recordSpan(t, tc.spanName, tc.attrKeys...)
			if got := IsAISpan(span); got != tc.want {
				t.Errorf("IsAISpan(%q) = %v, want %v", tc.spanName, got, tc.want)
			}
		})
	}
}

// recordSpanWithAttr records a span carrying a single string attribute.
func recordSpanWithAttr(t *testing.T, name, key, value string) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("test").Start(context.Background(), name)
	span.SetAttributes(attribute.String(key, value))
	span.End()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 recorded span, got %d", len(ended))
	}
	return ended[0]
}

func TestIsPostHogAIGatewayURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"gateway.us.posthog.com", true},
		{"https://gateway.us.posthog.com/v1", true},
		{"GATEWAY.US.POSTHOG.COM", true},
		{"ai-gateway.eu.posthog.com", true},
		{"gateway.us.posthog.com/v1/chat", true},
		{"api.openai.com", false},
		{"https://us.i.posthog.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isPostHogAIGatewayURL(tc.in); got != tc.want {
			t.Errorf("isPostHogAIGatewayURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWarnIfPostHogAIGateway(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	cases := []struct {
		name     string
		attrKey  string
		attrVal  string
		wantWarn bool
	}{
		{"server.address gateway host", "server.address", "gateway.us.posthog.com", true},
		{"url.full gateway host", "url.full", "https://ai-gateway.eu.posthog.com/v1/chat", true},
		{"non-gateway server.address", "server.address", "api.openai.com", false},
		{"gateway host on unrelated attribute", "http.url", "gateway.us.posthog.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			span := recordSpanWithAttr(t, "gen_ai.chat", tc.attrKey, tc.attrVal)
			warnIfPostHogAIGateway(span)
			warned := strings.Contains(buf.String(), "PostHog AI Gateway")
			if warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v (log=%q)", warned, tc.wantWarn, buf.String())
			}
		})
	}
}

func TestNewSpanProcessorRejectsEmptyAPIKey(t *testing.T) {
	if _, err := NewSpanProcessor(context.Background(), "  "); err != errEmptyAPIKey {
		t.Errorf("expected errEmptyAPIKey, got %v", err)
	}
}

func TestNewExporterRejectsEmptyAPIKey(t *testing.T) {
	if _, err := NewExporter(context.Background(), ""); err != errEmptyAPIKey {
		t.Errorf("expected errEmptyAPIKey, got %v", err)
	}
}

// otlpServer records the export requests it receives.
type otlpServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	auth    string
	path    string
	names   []string
	calls   int
	perCall []int
}

func newOTLPServer(t *testing.T) *otlpServer {
	t.Helper()
	s := &otlpServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := &coltracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("failed to decode OTLP request: %v", err)
		}
		s.mu.Lock()
		s.calls++
		s.auth = r.Header.Get("Authorization")
		s.path = r.URL.Path
		count := 0
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, span := range ss.GetSpans() {
					s.names = append(s.names, span.GetName())
					count++
				}
			}
		}
		s.perCall = append(s.perCall, count)
		s.mu.Unlock()

		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *otlpServer) snapshot() (calls int, auth, path string, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.auth, s.path, append([]string(nil), s.names...)
}

// batchSizes returns the number of spans carried by each export request, in
// the order the requests arrived.
func (s *otlpServer) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.perCall...)
}

// emitSpans sends one AI span and one non-AI span through the provider.
func emitSpans(provider *sdktrace.TracerProvider) {
	tracer := provider.Tracer("test")
	_, ai := tracer.Start(context.Background(), "gen_ai.chat")
	ai.End()
	_, other := tracer.Start(context.Background(), "http.request")
	other.End()
}

func TestSpanProcessorExportsOnlyAISpans(t *testing.T) {
	server := newOTLPServer(t)

	processor, err := NewSpanProcessor(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewSpanProcessor: %v", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
	emitSpans(provider)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, auth, path, names := server.snapshot()
	if want := "Bearer phc_test"; auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
	if want := ingestPath; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if len(names) != 1 || names[0] != "gen_ai.chat" {
		t.Errorf("exported span names = %v, want [gen_ai.chat]", names)
	}
}

func TestSpanProcessorDropsNonAISpansWithoutRequest(t *testing.T) {
	server := newOTLPServer(t)

	processor, err := NewSpanProcessor(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewSpanProcessor: %v", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))

	_, span := provider.Tracer("test").Start(context.Background(), "http.request")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if calls, _, _, _ := server.snapshot(); calls != 0 {
		t.Errorf("expected no export request, got %d", calls)
	}
}

func TestSpanProcessorKeepsBatchesWithinEndpointLimit(t *testing.T) {
	server := newOTLPServer(t)

	processor, err := NewSpanProcessor(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewSpanProcessor: %v", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))

	// Emit more AI spans than the endpoint accepts in a single request. With the
	// SDK default batch size (512) they would be sent as one oversized request
	// that the endpoint rejects with a non-retryable 400.
	const total = 2*maxSpansPerRequest + 5
	tracer := provider.Tracer("test")
	for i := 0; i < total; i++ {
		_, span := tracer.Start(context.Background(), "gen_ai.chat")
		span.End()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, _, _, names := server.snapshot()
	if len(names) != total {
		t.Errorf("exported %d spans, want %d", len(names), total)
	}
	for i, n := range server.batchSizes() {
		if n > maxSpansPerRequest {
			t.Errorf("request %d carried %d spans, exceeds endpoint limit %d", i, n, maxSpansPerRequest)
		}
	}
}

func TestExporterExportsOnlyAISpans(t *testing.T) {
	server := newOTLPServer(t)

	exporter, err := NewExporter(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	emitSpans(provider)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, _, path, names := server.snapshot()
	if want := ingestPath; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if len(names) != 1 || names[0] != "gen_ai.chat" {
		t.Errorf("exported span names = %v, want [gen_ai.chat]", names)
	}
}

func TestExporterKeepsBatchesWithinEndpointLimit(t *testing.T) {
	server := newOTLPServer(t)

	exporter, err := NewExporter(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	// WithBatcher uses the SDK default batch size (512), so without chunking the
	// exporter would hand more than the endpoint's limit to a single request.
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))

	const total = 2*maxSpansPerRequest + 5
	tracer := provider.Tracer("test")
	for i := 0; i < total; i++ {
		_, span := tracer.Start(context.Background(), "gen_ai.chat")
		span.End()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, _, _, names := server.snapshot()
	if len(names) != total {
		t.Errorf("exported %d spans, want %d", len(names), total)
	}
	for i, n := range server.batchSizes() {
		if n > maxSpansPerRequest {
			t.Errorf("request %d carried %d spans, exceeds endpoint limit %d", i, n, maxSpansPerRequest)
		}
	}
}

func TestExporterExportSpansSkipsRequestWhenNoAISpans(t *testing.T) {
	server := newOTLPServer(t)

	exporter, err := NewExporter(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer exporter.Shutdown(context.Background())

	span := recordSpan(t, "http.request")
	if err := exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}

	if calls, _, _, _ := server.snapshot(); calls != 0 {
		t.Errorf("expected no export request, got %d", calls)
	}
}

func TestWithHostFallsBackToDefault(t *testing.T) {
	cfg, err := newConfig(WithHost("  "))
	if err != nil {
		t.Fatalf("newConfig: %v", err)
	}
	if cfg.host != DefaultHost {
		t.Errorf("host = %q, want default %q", cfg.host, DefaultHost)
	}
	cfg, err = newConfig(WithHost("https://eu.i.posthog.com/"))
	if err != nil {
		t.Fatalf("newConfig: %v", err)
	}
	if cfg.host != "https://eu.i.posthog.com" {
		t.Errorf("host = %q, want trailing slash trimmed", cfg.host)
	}
}

func TestNewConfigRejectsInvalidHost(t *testing.T) {
	invalid := []string{
		"us.i.posthog.com",        // missing scheme
		"https://a b.example.com", // space in host
		"https://ex.com:port",     // invalid port
		"http://[::1",             // malformed host
		"ftp://example.com",       // wrong scheme
		"https://",                // no hostname
	}
	for _, host := range invalid {
		if _, err := newConfig(WithHost(host)); !errors.Is(err, errInvalidHost) {
			t.Errorf("newConfig(WithHost(%q)) err = %v, want errInvalidHost", host, err)
		}
	}

	valid := []string{
		"https://us.i.posthog.com",
		"https://eu.i.posthog.com/",
		"http://localhost:8000",
	}
	for _, host := range valid {
		if _, err := newConfig(WithHost(host)); err != nil {
			t.Errorf("newConfig(WithHost(%q)) err = %v, want nil", host, err)
		}
	}
}

func TestConstructorsRejectInvalidHost(t *testing.T) {
	if _, err := NewSpanProcessor(context.Background(), "phc_test", WithHost("us.i.posthog.com")); !errors.Is(err, errInvalidHost) {
		t.Errorf("NewSpanProcessor err = %v, want errInvalidHost", err)
	}
	if _, err := NewExporter(context.Background(), "phc_test", WithHost("us.i.posthog.com")); !errors.Is(err, errInvalidHost) {
		t.Errorf("NewExporter err = %v, want errInvalidHost", err)
	}
}
