package posthogotel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/posthog/posthog-go/v2"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestContextSpanProcessor(t *testing.T) {
	cases := []struct {
		name    string
		request posthog.RequestContext
		own     []attribute.KeyValue
		limit   int
		want    map[string]string
	}{
		{"both IDs", posthog.RequestContext{DistinctId: "user", SessionId: "session"}, nil, 128, map[string]string{"posthogDistinctId": "user", "sessionId": "session"}},
		{"session only", posthog.RequestContext{SessionId: "session"}, nil, 128, map[string]string{"sessionId": "session"}},
		{"no IDs", posthog.RequestContext{}, nil, 128, map[string]string{}},
		{"explicit IDs", posthog.RequestContext{DistinctId: "user", SessionId: "session"}, []attribute.KeyValue{attribute.String("posthogDistinctId", "app-user"), attribute.String("sessionId", "app-session")}, 128, map[string]string{"posthogDistinctId": "app-user", "sessionId": "app-session"}},
		{"full attributes", posthog.RequestContext{DistinctId: "user", SessionId: "session"}, []attribute.KeyValue{attribute.String("app.key", "keep")}, 1, map[string]string{"app.key": "keep"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			limits := sdktrace.NewSpanLimits()
			limits.AttributeCountLimit = tc.limit
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(newEnabledClient(t))), sdktrace.WithSpanProcessor(recorder), sdktrace.WithRawSpanLimits(limits))
			defer provider.Shutdown(context.Background())
			ctx := posthog.WithFreshRequestContext(context.Background(), tc.request)
			_, span := provider.Tracer("test").Start(ctx, "operation", trace.WithAttributes(tc.own...))
			span.End()
			assertContextAttributes(t, recorder.Ended()[0], tc.want)
		})
	}
}

func TestContextHTTPMiddleware(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprintf("panic=%t", panics), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(newEnabledClient(t))), sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			tracer := provider.Tracer("test")
			ctx, incoming := tracer.Start(context.Background(), "incoming")
			var detached context.Context
			var ongoing trace.Span
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				detached = context.WithValue(r.Context(), "test-key", "value")
				_, ongoing = tracer.Start(r.Context(), "child")
				if panics {
					panic("failure")
				}
				w.WriteHeader(http.StatusAccepted)
			})
			handler := NewRequestContextMiddleware(newEnabledClient(t), next)
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil).WithContext(ctx)
			request.Header.Set(posthog.HeaderPostHogDistinctID, "user")
			request.Header.Set(posthog.HeaderPostHogSessionID, "session")
			response := httptest.NewRecorder()
			func() {
				defer func() {
					if got := recover(); panics && got != "failure" {
						t.Errorf("panic=%v", got)
					} else if !panics && got != nil {
						t.Errorf("unexpected panic=%v", got)
					}
				}()
				handler.ServeHTTP(response, request)
			}()
			if !panics && response.Code != http.StatusAccepted {
				t.Errorf("status=%d", response.Code)
			}
			_, late := tracer.Start(detached, "late")
			late.End()
			ongoing.End()
			incoming.End()
			for _, span := range recorder.Ended() {
				want := map[string]string{"posthogDistinctId": "user", "sessionId": "session"}
				if span.Name() == "late" {
					want = map[string]string{}
				}
				assertContextAttributes(t, span, want)
			}
			fresh := posthog.WithFreshRequestContext(context.Background(), posthog.RequestContext{DistinctId: "job-user"})
			_, job := tracer.Start(fresh, "job")
			job.End()
			spans := recorder.Ended()
			assertContextAttributes(t, spans[len(spans)-1], map[string]string{"posthogDistinctId": "job-user"})
		})
	}
}

func TestContextHTTPMiddlewareOptions(t *testing.T) {
	cases := []struct {
		name, distinctID, sessionID string
		disabled                    bool
		want                        map[string]string
	}{
		{"disabled", "user", "session", true, map[string]string{}},
		{"absent", "", "", false, map[string]string{}},
		{"invalid", "\x00", "  ", false, map[string]string{}},
		{"sanitized", "bad\x00user", " session ", false, map[string]string{"posthogDistinctId": "baduser", "sessionId": "session"}},
		{"session only", "", "session", false, map[string]string{"sessionId": "session"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(newEnabledClient(t))), sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			ctx, incoming := provider.Tracer("test").Start(context.Background(), "incoming")
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil).WithContext(ctx)
			request.Header.Set(posthog.HeaderPostHogDistinctID, tc.distinctID)
			request.Header.Set(posthog.HeaderPostHogSessionID, tc.sessionID)
			NewRequestContextMiddleware(newEnabledClient(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), posthog.WithCaptureTracingHeaders(!tc.disabled)).ServeHTTP(httptest.NewRecorder(), request)
			incoming.End()
			assertContextAttributes(t, recorder.Ended()[0], tc.want)
		})
	}
}

func TestContextHTTPMiddlewareConcurrentRequests(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(newEnabledClient(t))), sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	tracer := provider.Tracer("test")
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := NewRequestContextMiddleware(newEnabledClient(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ready <- struct{}{}
		<-release
		_, span := tracer.Start(r.Context(), r.Header.Get(posthog.HeaderPostHogDistinctID))
		span.End()
	}))
	var workers sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
			request.Header.Set(posthog.HeaderPostHogDistinctID, id)
			request.Header.Set(posthog.HeaderPostHogSessionID, id+"-session")
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}(id)
	}
	<-ready
	<-ready
	close(release)
	workers.Wait()
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded spans=%d, want 2", len(spans))
	}
	for _, span := range spans {
		assertContextAttributes(t, span, map[string]string{"posthogDistinctId": span.Name(), "sessionId": span.Name() + "-session"})
	}
}

func assertContextAttributes(t *testing.T, span sdktrace.ReadOnlySpan, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, attr := range span.Attributes() {
		got[string(attr.Key)] = attr.Value.AsString()
	}
	if len(got) != len(want) {
		t.Fatalf("span %s attributes=%v, want %v", span.Name(), got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("span %s %s=%q, want %q", span.Name(), key, got[key], value)
		}
	}
}

func TestContextHTTPMiddlewareWithoutRecording(t *testing.T) {
	for _, sampledOut := range []bool{false, true} {
		t.Run(fmt.Sprintf("sampledOut=%t", sampledOut), func(t *testing.T) {
			ctx := context.Background()
			if sampledOut {
				provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()), sdktrace.WithSpanProcessor(NewContextSpanProcessor(newEnabledClient(t))))
				defer provider.Shutdown(context.Background())
				var span trace.Span
				ctx, span = provider.Tracer("test").Start(ctx, "unsampled")
				defer span.End()
			}
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil).WithContext(ctx)
			request.Header.Set(posthog.HeaderPostHogSessionID, "session")
			response := httptest.NewRecorder()
			NewRequestContextMiddleware(newEnabledClient(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				metadata, ok := posthog.RequestContextFromContext(r.Context())
				if !ok || metadata.SessionId != "session" {
					t.Errorf("event context=%+v", metadata)
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Errorf("status=%d", response.Code)
			}
		})
	}
}

type contextOpaqueSpan struct{ trace.Span }

func TestContextHTTPMiddlewareKeepsUnreadableSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	ctx, span := provider.Tracer("test").Start(context.Background(), "opaque", trace.WithAttributes(attribute.String("sessionId", "application-session")))
	ctx = trace.ContextWithSpan(ctx, contextOpaqueSpan{span})
	request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil).WithContext(ctx)
	request.Header.Set(posthog.HeaderPostHogSessionID, "header-session")
	NewRequestContextMiddleware(newEnabledClient(t), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), request)
	span.End()
	assertContextAttributes(t, recorder.Ended()[0], map[string]string{"sessionId": "application-session"})
}

func TestContextSpanProcessorShutdown(t *testing.T) {
	for _, calls := range []int{1, 2} {
		t.Run(fmt.Sprintf("shutdownCalls=%d", calls), func(t *testing.T) {
			processor := NewContextSpanProcessor(newEnabledClient(t))
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor), sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			ctx := posthog.WithFreshRequestContext(context.Background(), posthog.RequestContext{DistinctId: "user", SessionId: "session"})
			tracer := provider.Tracer("test")
			_, before := tracer.Start(ctx, "before")
			for range calls {
				if err := processor.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			_, after := tracer.Start(ctx, "after")
			after.End()
			before.End()
			spans := recorder.Ended()
			if len(spans) != 2 {
				t.Fatalf("recorded spans=%d, want 2", len(spans))
			}
			assertContextAttributes(t, spans[0], map[string]string{})
			assertContextAttributes(t, spans[1], map[string]string{"posthogDistinctId": "user", "sessionId": "session"})
		})
	}
}

type contextClientWrapper struct{ posthog.Client }

func newClient(t *testing.T, apiKey string) posthog.Client {
	t.Helper()
	client, err := posthog.New(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newEnabledClient(t *testing.T) posthog.Client {
	t.Helper()
	client := newClient(t, "test-key")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestContextSpansFollowClientState(t *testing.T) {
	cases := []struct {
		name   string
		client func(*testing.T) posthog.Client
		want   map[string]string
	}{
		{"missing client", func(*testing.T) posthog.Client { return nil }, map[string]string{}},
		{"empty API key", func(t *testing.T) posthog.Client { return newClient(t, " ") }, map[string]string{}},
		{"closed client", func(t *testing.T) posthog.Client {
			client := newClient(t, "test-key")
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			return client
		}, map[string]string{}},
		{"enabled client", newEnabledClient, map[string]string{"posthogDistinctId": "user", "sessionId": "session"}},
		{"wrapper without IsEnabled", func(t *testing.T) posthog.Client { return contextClientWrapper{newClient(t, "")} }, map[string]string{"posthogDistinctId": "user", "sessionId": "session"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/processor", func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(tc.client(t))), sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			ctx := posthog.WithFreshRequestContext(context.Background(), posthog.RequestContext{DistinctId: "user", SessionId: "session"})
			_, span := provider.Tracer("test").Start(ctx, "operation")
			span.End()
			assertContextAttributes(t, recorder.Ended()[0], tc.want)
		})
		t.Run(tc.name+"/middleware", func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			ctx, incoming := provider.Tracer("test").Start(context.Background(), "incoming")
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil).WithContext(ctx)
			request.Header.Set(posthog.HeaderPostHogDistinctID, "user")
			request.Header.Set(posthog.HeaderPostHogSessionID, "session")
			NewRequestContextMiddleware(tc.client(t), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), request)
			incoming.End()
			assertContextAttributes(t, recorder.Ended()[0], tc.want)
		})
	}
}

func TestContextSpanProcessorClientsAreIndependent(t *testing.T) {
	closed, open := newClient(t, "first-key"), newEnabledClient(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := posthog.WithFreshRequestContext(context.Background(), posthog.RequestContext{DistinctId: "user", SessionId: "session"})
	for _, tc := range []struct {
		name   string
		client posthog.Client
		want   map[string]string
	}{
		{"closed", closed, map[string]string{}},
		{"open", open, map[string]string{"posthogDistinctId": "user", "sessionId": "session"}},
	} {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(NewContextSpanProcessor(tc.client)), sdktrace.WithSpanProcessor(recorder))
		_, span := provider.Tracer("test").Start(ctx, tc.name)
		span.End()
		_ = provider.Shutdown(context.Background())
		assertContextAttributes(t, recorder.Ended()[0], tc.want)
	}
}
