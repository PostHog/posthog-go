package posthog

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEnqueueWithContext_AppliesRequestContextHeadersAndMetadata(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	req := httptest.NewRequest(http.MethodPost, "https://example.com/api/test?oauth_code=secret", nil)
	req.RemoteAddr = "10.0.0.2:4567"
	req.Header.Set("x-posthog-distinct-id", " frontend-user ")
	req.Header.Set("x-posthog-session-id", " frontend-session ")
	req.Header.Set("User-Agent", "TestAgent/1.0")
	ctx := WithFreshRequestContext(context.Background(), ExtractRequestContext(req, true))

	err := EnqueueWithContext(ctx, client, Capture{Event: "request-context-event"})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	require.Equal(t, "frontend-user", event["distinct_id"])
	properties := requireProperties(t, event)
	require.Equal(t, "frontend-session", properties[propertySessionID])
	require.Equal(t, "https://example.com/api/test", properties[propertyCurrentURL])
	require.Equal(t, http.MethodPost, properties[propertyRequestMethod])
	require.Equal(t, "/api/test", properties[propertyRequestPath])
	require.Equal(t, "TestAgent/1.0", properties[propertyUserAgent])
	require.Equal(t, "10.0.0.2", properties[propertyIP])
	require.NotContains(t, properties, propertyProcessPersonProfile)
}

func TestEnqueueWithContext_ExplicitCaptureValuesOverrideContext(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{
		DistinctId: "context-user",
		SessionId:  "context-session",
		Properties: NewProperties().Set("shared", "context-value").Set("context-only", "context-only-value"),
	})

	err := EnqueueWithContext(ctx, client, Capture{
		DistinctId: "explicit-user",
		Event:      "explicit-event",
		Properties: NewProperties().Set("shared", "explicit-value").Set(propertySessionID, "explicit-session"),
	})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	require.Equal(t, "explicit-user", event["distinct_id"])
	properties := requireProperties(t, event)
	require.Equal(t, "explicit-session", properties[propertySessionID])
	require.Equal(t, "explicit-value", properties["shared"])
	require.Equal(t, "context-only-value", properties["context-only"])
	require.NotContains(t, properties, propertyProcessPersonProfile)
}

func TestEnqueue_MissingIdentityWithoutRequestContextReturnsDistinctIdError(t *testing.T) {
	client, _ := NewWithConfig("test-key", Config{Transport: NoOpTransport()})
	defer client.Close()

	err := client.Enqueue(Capture{Event: "personless-event"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DistinctId")
}

func TestEnqueueWithContext_MissingIdentityWithRequestContextCreatesPersonlessCapture(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{})
	err := EnqueueWithContext(ctx, client, Capture{Event: "personless-event"})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	distinctID, ok := event["distinct_id"].(string)
	require.True(t, ok)
	require.NoError(t, uuid.Validate(distinctID))
	properties := requireProperties(t, event)
	require.Equal(t, false, properties[propertyProcessPersonProfile])
}

func TestEnqueueWithContext_PersonlessCapturePreservesExplicitProcessPersonProfile(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{})
	err := EnqueueWithContext(ctx, client, Capture{
		Event:      "personless-event",
		Properties: NewProperties().Set(propertyProcessPersonProfile, true),
	})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	require.Equal(t, true, properties[propertyProcessPersonProfile])
}

func TestEnqueueWithContext_PersonlessCaptureSkipsSendFeatureFlags(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	logger := &captureLogger{}
	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime, Logger: logger})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{})
	err := EnqueueWithContext(ctx, client, Capture{
		Event:            "personless-event",
		SendFeatureFlags: SendFeatureFlags(true),
	})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	require.NotContains(t, properties, "$active_feature_flags")
	for key := range properties {
		require.False(t, strings.HasPrefix(key, "$feature/"), "personless capture should not attach feature flag properties")
	}
	for _, warning := range logger.snapshot() {
		require.NotContains(t, warning, "Capture.SendFeatureFlags")
	}
}

func TestEnqueueWithContext_ExceptionUsesContextAndExplicitIdentity(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{
		DistinctId: "context-user",
		SessionId:  "context-session",
		Properties: NewProperties().Set("request", "metadata"),
	})

	err := EnqueueWithContext(ctx, client, Exception{
		DistinctId: "explicit-user",
		ExceptionList: []ExceptionItem{{
			Type:  "Error",
			Value: "boom",
		}},
	})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	require.Equal(t, "$exception", event["event"])
	properties := requireProperties(t, event)
	require.Equal(t, "explicit-user", properties["distinct_id"])
	require.Equal(t, "context-session", properties[propertySessionID])
	require.Equal(t, "metadata", properties["request"])
	require.NotContains(t, properties, propertyProcessPersonProfile)
}

func TestEnqueueWithContext_ExceptionFallsBackToRequestContext(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{
		DistinctId: "context-user",
		SessionId:  "context-session",
	})

	err := EnqueueWithContext(ctx, client, Exception{ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	require.Equal(t, "context-user", properties["distinct_id"])
	require.Equal(t, "context-session", properties[propertySessionID])
	require.NotContains(t, properties, propertyProcessPersonProfile)
}

func TestEnqueue_ExceptionMissingIdentityWithoutRequestContextReturnsDistinctIdError(t *testing.T) {
	client, _ := NewWithConfig("test-key", Config{Transport: NoOpTransport()})
	defer client.Close()

	err := client.Enqueue(Exception{ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DistinctId")
}

func TestEnqueueWithContext_ExceptionMissingIdentityWithRequestContextCreatesPersonlessEvent(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{})
	err := EnqueueWithContext(ctx, client, Exception{ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	distinctID, ok := properties["distinct_id"].(string)
	require.True(t, ok)
	require.NoError(t, uuid.Validate(distinctID))
	require.Equal(t, false, properties[propertyProcessPersonProfile])
}

func TestRequestContextMiddleware_DisableTracingHeadersPreservesMetadata(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	handler := NewRequestContextMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requestContext, ok := RequestContextFromContext(r.Context())
		require.True(t, ok)
		require.Empty(t, requestContext.DistinctId)
		require.Empty(t, requestContext.SessionId)
		require.NoError(t, EnqueueWithContext(r.Context(), client, Capture{Event: "metadata-event"}))
	}), WithCaptureTracingHeaders(false))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/test?token=secret", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set(HeaderPostHogDistinctID, "header-user")
	req.Header.Set(HeaderPostHogSessionID, "header-session")
	req.Header.Set("User-Agent", "TestAgent/1.0")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	event := readSingleBatchEvent(t, body)
	distinctID, ok := event["distinct_id"].(string)
	require.True(t, ok)
	require.NotEqual(t, "header-user", distinctID)
	require.NoError(t, uuid.Validate(distinctID))
	properties := requireProperties(t, event)
	require.Equal(t, false, properties[propertyProcessPersonProfile])
	require.NotContains(t, properties, propertySessionID)
	require.Equal(t, "/api/test", properties[propertyRequestPath])
	require.Equal(t, "TestAgent/1.0", properties[propertyUserAgent])
	require.Equal(t, "10.0.0.2", properties[propertyIP])
}

func TestRequestContextMiddleware_SanitizesHeaders(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	handler := NewRequestContextMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.NoError(t, EnqueueWithContext(r.Context(), client, Capture{Event: "sanitized-event"}))
	}))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/test", nil)
	req.Header.Set(HeaderPostHogDistinctID, " \u0000\u0001\u0002 ")
	req.Header.Set(HeaderPostHogSessionID, "  "+strings.Repeat("s", 1200)+"\u0000  ")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	event := readSingleBatchEvent(t, body)
	distinctID, ok := event["distinct_id"].(string)
	require.True(t, ok)
	require.NoError(t, uuid.Validate(distinctID))
	properties := requireProperties(t, event)
	require.Equal(t, strings.Repeat("s", maxRequestContextValueLength), properties[propertySessionID])
}

func TestRequestContextMiddleware_ConcurrentRequestsDoNotLeak(t *testing.T) {
	results := map[string]RequestContext{}
	var mu sync.Mutex

	handler := NewRequestContextMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		requestContext, ok := RequestContextFromContext(r.Context())
		require.True(t, ok)
		mu.Lock()
		results[r.URL.Path] = requestContext
		mu.Unlock()
	}))

	first := httptest.NewRequest(http.MethodGet, "https://example.com/first", nil)
	first.Header.Set(HeaderPostHogDistinctID, "user-a")
	first.Header.Set(HeaderPostHogSessionID, "session-a")
	second := httptest.NewRequest(http.MethodGet, "https://example.com/second", nil)
	second.Header.Set(HeaderPostHogDistinctID, "user-b")
	second.Header.Set(HeaderPostHogSessionID, "session-b")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(httptest.NewRecorder(), first)
	}()
	go func() {
		defer wg.Done()
		handler.ServeHTTP(httptest.NewRecorder(), second)
	}()
	wg.Wait()

	require.Equal(t, "user-a", results["/first"].DistinctId)
	require.Equal(t, "session-a", results["/first"].SessionId)
	require.Equal(t, "user-b", results["/second"].DistinctId)
	require.Equal(t, "session-b", results["/second"].SessionId)
}

func TestEnqueueWithContext_PersonlessCaptureDefaultPropertiesCannotEnablePersonProfiles(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		Endpoint:               server.URL,
		BatchSize:              1,
		now:                    mockTime,
		DefaultEventProperties: NewProperties().Set(propertyProcessPersonProfile, true).Set("service", "api"),
	})
	defer client.Close()

	ctx := WithFreshRequestContext(context.Background(), RequestContext{})
	err := EnqueueWithContext(ctx, client, Capture{Event: "personless-event"})
	require.NoError(t, err)

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	require.Equal(t, false, properties[propertyProcessPersonProfile])
	require.Equal(t, "api", properties["service"])
}

func TestParseDebugStackFramesUsesCanonicalBottomUpOrder(t *testing.T) {
	// Regression guard for the canonical wire order on the panic-recovery
	// path: debug.Stack prints innermost first, so without the reversal the
	// panic site would sit at frames[0] while GetStackTrace puts it last.
	var stack []byte
	func() {
		defer func() {
			recover()
			stack = debug.Stack()
		}()
		panic("boom")
	}()

	frames := parseDebugStackFrames(stack)
	if len(frames) < 2 {
		t.Fatalf("expected multiple frames, got %d", len(frames))
	}

	first := frames[0].Function
	last := frames[len(frames)-1].Function
	if !strings.Contains(last, "TestParseDebugStackFramesUsesCanonicalBottomUpOrder") {
		t.Fatalf("expected panic-adjacent frame last, got first=%q last=%q", first, last)
	}
	if strings.Contains(first, "TestParseDebugStackFramesUsesCanonicalBottomUpOrder.func") {
		t.Fatalf("panic site must not be first: first=%q", first)
	}
}

func TestRequestContextMiddleware_CapturesPanicsWithRequestContextAndRethrows(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	handler := NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		panic(errors.New("boom"))
	}), WithCapturePanics(client))

	req := httptest.NewRequest(http.MethodPost, "https://example.com/api/test", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set(HeaderPostHogDistinctID, "client-user")
	req.Header.Set(HeaderPostHogSessionID, "client-session")
	require.Panics(t, func() { handler.ServeHTTP(httptest.NewRecorder(), req) })

	event := readSingleBatchEvent(t, body)
	require.Equal(t, "$exception", event["event"])
	properties := requireProperties(t, event)
	require.Equal(t, "client-user", properties["distinct_id"])
	require.Equal(t, "client-session", properties[propertySessionID])
	require.Equal(t, float64(http.StatusServiceUnavailable), properties[propertyResponseStatusCode])
	require.Equal(t, "panic", properties[propertyExceptionSource])
	requireExceptionStackContainsFunction(t, properties, "TestRequestContextMiddleware_CapturesPanicsWithRequestContextAndRethrows")
}

func TestRequestContextMiddleware_PanicCaptureFailuresDoNotReplaceOriginalPanic(t *testing.T) {
	originalPanic := errors.New("original application panic")

	tests := []struct {
		name string
		opts []RequestContextOption
	}{
		{
			name: "enqueue panics",
			opts: []RequestContextOption{WithCapturePanics(panickingEnqueueClient{})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewRequestContextMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic(originalPanic)
			}), tt.opts...)

			req := httptest.NewRequest(http.MethodGet, "https://example.com/panic", nil)
			require.PanicsWithValue(t, originalPanic, func() {
				handler.ServeHTTP(httptest.NewRecorder(), req)
			})
		})
	}
}

func TestRequestContextMiddleware_PanicCaptureUsesFirstResponseStatus(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{Endpoint: server.URL, BatchSize: 1, now: mockTime})
	defer client.Close()

	handler := NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.WriteHeader(http.StatusTeapot)
		panic(errors.New("boom"))
	}), WithCapturePanics(client))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/panic", nil)
	require.Panics(t, func() { handler.ServeHTTP(httptest.NewRecorder(), req) })

	event := readSingleBatchEvent(t, body)
	properties := requireProperties(t, event)
	require.Equal(t, float64(http.StatusTooManyRequests), properties[propertyResponseStatusCode])
}

func TestResponseStatusWriterTracksFirstStatusAndUnwraps(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseStatusWriter{ResponseWriter: recorder, statusCode: http.StatusOK}

	require.Same(t, recorder, writer.Unwrap())
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusInternalServerError)
	require.Equal(t, http.StatusAccepted, writer.statusCode)
}

func TestRequestContextMiddleware_PanicWriterDoesNotAdvertiseUnsupportedOptionalInterfaces(t *testing.T) {
	var report optionalInterfaceReport
	handler := NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		report = captureOptionalInterfaceReport(w)
	}), WithCapturePanics(&noopClient{}))

	handler.ServeHTTP(newMinimalResponseWriter(), httptest.NewRequest(http.MethodGet, "https://example.com", nil))

	require.False(t, report.flusher)
	require.False(t, report.hijacker)
	require.False(t, report.pusher)
	require.False(t, report.readerFrom)
}

func TestRequestContextMiddleware_PanicWriterPreservesSupportedOptionalInterfaces(t *testing.T) {
	writer := &allOptionalResponseWriter{minimalResponseWriter: newMinimalResponseWriter()}
	var report optionalInterfaceReport
	handler := NewRequestContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		report = captureOptionalInterfaceReport(w)
		w.(http.Flusher).Flush()
		_, _, _ = w.(http.Hijacker).Hijack()
		require.NoError(t, w.(http.Pusher).Push("/asset.css", nil))
		_, err := w.(io.ReaderFrom).ReadFrom(strings.NewReader("body"))
		require.NoError(t, err)
	}), WithCapturePanics(&noopClient{}))

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "https://example.com", nil))

	require.True(t, report.flusher)
	require.True(t, report.hijacker)
	require.True(t, report.pusher)
	require.True(t, report.readerFrom)
	require.True(t, writer.flushed)
	require.True(t, writer.hijacked)
	require.Equal(t, "/asset.css", writer.pushedTarget)
	require.True(t, writer.readFromCalled)
}

type panickingEnqueueClient struct{}

func (panickingEnqueueClient) Enqueue(Message) error {
	panic("enqueue panic")
}

func (panickingEnqueueClient) EnqueueWithContext(context.Context, Message) error {
	panic("enqueue with context panic")
}

func readSingleBatchEvent(t *testing.T, body <-chan []byte) map[string]interface{} {
	t.Helper()

	select {
	case payload := <-body:
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal(payload, &decoded))
		batch, ok := decoded["batch"].([]interface{})
		require.True(t, ok)
		require.Len(t, batch, 1)
		event, ok := batch[0].(map[string]interface{})
		require.True(t, ok)
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for batch payload")
		return nil
	}
}

func requireProperties(t *testing.T, event map[string]interface{}) map[string]interface{} {
	t.Helper()
	properties, ok := event["properties"].(map[string]interface{})
	require.True(t, ok)
	return properties
}

func requireExceptionStackContainsFunction(t *testing.T, properties map[string]interface{}, functionName string) {
	t.Helper()
	exceptionList, ok := properties["$exception_list"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, exceptionList)
	exceptionItem, ok := exceptionList[0].(map[string]interface{})
	require.True(t, ok)
	stacktrace, ok := exceptionItem["stacktrace"].(map[string]interface{})
	require.True(t, ok)
	frames, ok := stacktrace["frames"].([]interface{})
	require.True(t, ok)
	for _, rawFrame := range frames {
		frame, ok := rawFrame.(map[string]interface{})
		require.True(t, ok)
		function, _ := frame["function"].(string)
		if strings.Contains(function, functionName) {
			return
		}
	}
	require.Failf(t, "missing panic frame", "expected stacktrace to contain function %q, got %#v", functionName, frames)
}

type optionalInterfaceReport struct {
	flusher    bool
	hijacker   bool
	pusher     bool
	readerFrom bool
}

func captureOptionalInterfaceReport(w http.ResponseWriter) optionalInterfaceReport {
	_, flusher := w.(http.Flusher)
	_, hijacker := w.(http.Hijacker)
	_, pusher := w.(http.Pusher)
	_, readerFrom := w.(io.ReaderFrom)
	return optionalInterfaceReport{
		flusher:    flusher,
		hijacker:   hijacker,
		pusher:     pusher,
		readerFrom: readerFrom,
	}
}

type minimalResponseWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func newMinimalResponseWriter() *minimalResponseWriter {
	return &minimalResponseWriter{header: http.Header{}, status: http.StatusOK}
}

func (w *minimalResponseWriter) Header() http.Header { return w.header }

func (w *minimalResponseWriter) Write(data []byte) (int, error) {
	return w.body.Write(data)
}

func (w *minimalResponseWriter) WriteHeader(statusCode int) { w.status = statusCode }

type allOptionalResponseWriter struct {
	*minimalResponseWriter
	flushed        bool
	hijacked       bool
	pushedTarget   string
	readFromCalled bool
}

func (w *allOptionalResponseWriter) Flush() { w.flushed = true }

func (w *allOptionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

func (w *allOptionalResponseWriter) Push(target string, _ *http.PushOptions) error {
	w.pushedTarget = target
	return nil
}

func (w *allOptionalResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	w.readFromCalled = true
	return io.Copy(w.minimalResponseWriter, reader)
}
