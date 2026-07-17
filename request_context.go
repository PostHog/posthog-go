package posthog

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	// HeaderPostHogDistinctID is the HTTP header used to propagate a PostHog distinct ID.
	HeaderPostHogDistinctID = "X-PostHog-Distinct-Id"
	// HeaderPostHogSessionID is the HTTP header used to propagate a PostHog session ID.
	HeaderPostHogSessionID = "X-PostHog-Session-Id"

	propertyProcessPersonProfile = "$process_person_profile"
	propertySessionID            = "$session_id"
	propertyWindowID             = "$window_id"
	propertyCurrentURL           = "$current_url"
	propertyRequestMethod        = "$request_method"
	propertyRequestPath          = "$request_path"
	propertyUserAgent            = "$user_agent"
	propertyIP                   = "$ip"
	propertyResponseStatusCode   = "$response_status_code"
	propertyExceptionSource      = "$exception_source"

	maxRequestContextValueLength = 1000
)

type requestContextKey struct{}

// RequestContext carries request-scoped PostHog metadata that is applied to capture and exception events
// enqueued with EnqueueWithContext.
//
// DistinctId and SessionId usually come from PostHog tracing headers. Properties usually contain safe
// request metadata such as $current_url, $request_method, $request_path, $user_agent, and $ip.
type RequestContext struct {
	// DistinctId is the request-scoped user distinct ID.
	DistinctId string
	// SessionId is the request-scoped PostHog session ID.
	SessionId string
	// Properties are request-scoped properties merged into capture and exception events.
	Properties Properties
}

// WithRequestContext returns a child context with PostHog request metadata attached. Existing PostHog
// request context values are inherited: non-empty DistinctId and SessionId override the parent values,
// and Properties are merged with child properties taking precedence.
func WithRequestContext(ctx context.Context, requestContext RequestContext) context.Context {
	return withRequestContext(ctx, requestContext, false)
}

// WithFreshRequestContext returns a child context with PostHog request metadata attached without inheriting
// any existing PostHog request context from the parent.
func WithFreshRequestContext(ctx context.Context, requestContext RequestContext) context.Context {
	return withRequestContext(ctx, requestContext, true)
}

func withRequestContext(ctx context.Context, requestContext RequestContext, fresh bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	var base RequestContext
	if !fresh {
		base, _ = RequestContextFromContext(ctx)
	}

	merged := RequestContext{
		DistinctId: sanitizeRequestContextValue(base.DistinctId),
		SessionId:  sanitizeRequestContextValue(base.SessionId),
		Properties: cloneRequestProperties(base.Properties),
	}

	if distinctID := sanitizeRequestContextValue(requestContext.DistinctId); distinctID != "" {
		merged.DistinctId = distinctID
	}
	if sessionID := sanitizeRequestContextValue(requestContext.SessionId); sessionID != "" {
		merged.SessionId = sessionID
	}
	if len(requestContext.Properties) > 0 {
		if merged.Properties == nil {
			merged.Properties = NewProperties()
		}
		for key, value := range requestContext.Properties {
			if key == "" {
				continue
			}
			merged.Properties[key] = sanitizePropertyValue(value)
		}
	}

	return context.WithValue(ctx, requestContextKey{}, merged)
}

// RequestContextFromContext returns the PostHog request metadata attached to ctx, if any.
func RequestContextFromContext(ctx context.Context) (RequestContext, bool) {
	if ctx == nil {
		return RequestContext{}, false
	}
	requestContext, ok := ctx.Value(requestContextKey{}).(RequestContext)
	if !ok {
		return RequestContext{}, false
	}
	return RequestContext{
		DistinctId: sanitizeRequestContextValue(requestContext.DistinctId),
		SessionId:  sanitizeRequestContextValue(requestContext.SessionId),
		Properties: cloneRequestProperties(requestContext.Properties),
	}, true
}

// RequestContextOptions configures NewRequestContextMiddleware.
type RequestContextOptions struct {
	// CaptureTracingHeaders controls whether X-PostHog-Distinct-Id and X-PostHog-Session-Id
	// are read into request-scoped analytics context. It defaults to true in NewRequestContextMiddleware.
	// When disabled, the middleware still attaches request metadata to the request context.
	CaptureTracingHeaders bool

	// CapturePanics controls whether NewRequestContextMiddleware captures downstream panics as
	// PostHog exception events before re-panicking. It defaults to false.
	CapturePanics bool

	// PanicCaptureClient receives panic exception events when CapturePanics is enabled.
	PanicCaptureClient EnqueueClient
}

// RequestContextOption customizes request context middleware.
type RequestContextOption func(*RequestContextOptions)

// WithCaptureTracingHeaders configures whether request context middleware reads PostHog tracing headers.
func WithCaptureTracingHeaders(enabled bool) RequestContextOption {
	return func(options *RequestContextOptions) {
		options.CaptureTracingHeaders = enabled
	}
}

// WithCapturePanics configures request context middleware to capture downstream panics as
// PostHog exception events using client, then re-panic with the original value.
func WithCapturePanics(client EnqueueClient) RequestContextOption {
	return func(options *RequestContextOptions) {
		options.CapturePanics = client != nil
		options.PanicCaptureClient = client
	}
}

func defaultRequestContextOptions() RequestContextOptions {
	return RequestContextOptions{CaptureTracingHeaders: true}
}

// NewRequestContextMiddleware wraps an http.Handler with PostHog request-context extraction.
//
// The middleware always attaches safe request metadata to r.Context(). By default it also reads
// X-PostHog-Distinct-Id and X-PostHog-Session-Id into request-scoped analytics context. Use
// EnqueueWithContext(r.Context(), ...) for captures inside the request so this metadata is applied.
func NewRequestContextMiddleware(next http.Handler, opts ...RequestContextOption) http.Handler {
	options := defaultRequestContextOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			return
		}
		if r == nil {
			return
		}

		requestContext := ExtractRequestContext(r, options.CaptureTracingHeaders)
		ctx := WithFreshRequestContext(r.Context(), requestContext)
		req := r.WithContext(ctx)

		if !options.CapturePanics {
			next.ServeHTTP(w, req)
			return
		}

		statusWriter := newResponseStatusWriter(w)
		completed := false
		defer func() {
			if completed {
				return
			}

			panicStack := debug.Stack()
			panicValue := recover()
			if panicValue == nil {
				return
			}
			capturePanic(ctx, options.PanicCaptureClient, panicValue, statusWriter.statusCode, panicStack)
			panic(panicValue)
		}()

		next.ServeHTTP(wrapResponseStatusWriter(statusWriter), req)
		completed = true
	})
}

// ExtractRequestContext extracts sanitized PostHog request context from an HTTP request. Request metadata
// is always extracted. Tracing headers are only read when captureTracingHeaders is true.
func ExtractRequestContext(r *http.Request, captureTracingHeaders bool) RequestContext {
	if r == nil {
		return RequestContext{Properties: NewProperties()}
	}

	properties := NewProperties()
	addPropertyIfPresent(properties, propertyCurrentURL, currentURL(r))
	addPropertyIfPresent(properties, propertyRequestMethod, r.Method)
	addPropertyIfPresent(properties, propertyRequestPath, requestPath(r))
	addPropertyIfPresent(properties, propertyUserAgent, r.Header.Get("User-Agent"))
	addPropertyIfPresent(properties, propertyIP, remoteIP(r))

	var distinctID, sessionID string
	if captureTracingHeaders {
		distinctID = sanitizeRequestContextValue(r.Header.Get(HeaderPostHogDistinctID))
		sessionID = sanitizeRequestContextValue(r.Header.Get(HeaderPostHogSessionID))
	}

	return RequestContext{
		DistinctId: distinctID,
		SessionId:  sessionID,
		Properties: properties,
	}
}

type captureContext struct {
	distinctID                    string
	properties                    Properties
	generatedPersonlessDistinctID bool
	personlessProcessProfileGuard bool
}

func resolveCaptureContext(ctx context.Context, distinctID string, properties Properties, fieldType string) (captureContext, error) {
	requestContext, hasRequestContext := RequestContextFromContext(ctx)
	resolvedDistinctID := distinctID
	isPersonless := false
	if resolvedDistinctID == "" && hasRequestContext {
		resolvedDistinctID = requestContext.DistinctId
	}
	if resolvedDistinctID == "" {
		if !hasRequestContext {
			return captureContext{}, FieldError{
				Type:  fieldType,
				Name:  "DistinctId",
				Value: distinctID,
			}
		}
		resolvedDistinctID = makeUUID("")
		isPersonless = true
	}

	resolved := captureContext{
		distinctID:                    resolvedDistinctID,
		properties:                    mergeContextProperties(requestContext, properties),
		generatedPersonlessDistinctID: isPersonless,
	}
	if isPersonless {
		if resolved.properties == nil {
			resolved.properties = NewProperties()
		}
		if _, exists := resolved.properties[propertyProcessPersonProfile]; !exists {
			resolved.properties[propertyProcessPersonProfile] = false
			resolved.personlessProcessProfileGuard = true
		}
	}

	return resolved, nil
}

func mergeContextProperties(requestContext RequestContext, explicit Properties) Properties {
	var merged Properties
	if len(requestContext.Properties) > 0 {
		merged = cloneRequestProperties(requestContext.Properties)
	}
	if requestContext.SessionId != "" {
		if merged == nil {
			merged = NewProperties()
		}
		if _, exists := merged[propertySessionID]; !exists {
			merged[propertySessionID] = requestContext.SessionId
		}
	}
	if len(explicit) > 0 {
		if merged == nil {
			merged = NewProperties()
		}
		for key, value := range explicit {
			merged[key] = value
		}
	}
	return merged
}

func cloneRequestProperties(properties Properties) Properties {
	if len(properties) == 0 {
		return nil
	}
	cloned := make(Properties, len(properties))
	for key, value := range properties {
		cloned[key] = value
	}
	return cloned
}

func sanitizePropertyValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	if stringValue, ok := value.(string); ok {
		return sanitizeRequestContextValue(stringValue)
	}
	return value
}

func addPropertyIfPresent(properties Properties, key, value string) {
	if value = sanitizeRequestContextValue(value); value != "" {
		properties.Set(key, value)
	}
}

func sanitizeRequestContextValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}

	builder := strings.Builder{}
	builder.Grow(len(value))
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			continue
		}
		builder.WriteRune(character)
	}

	sanitized := strings.TrimSpace(builder.String())
	if sanitized == "" {
		return ""
	}
	runes := []rune(sanitized)
	if len(runes) > maxRequestContextValueLength {
		return string(runes[:maxRequestContextValueLength])
	}
	return sanitized
}

func currentURL(r *http.Request) string {
	scheme := requestScheme(r)
	host := requestHost(r)
	if scheme == "" || host == "" {
		return ""
	}
	return scheme + "://" + host + requestPath(r)
}

func requestScheme(r *http.Request) string {
	if r.URL != nil && r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	if r.URL != nil {
		return r.URL.Host
	}
	return ""
}

func requestPath(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	path := r.URL.EscapedPath()
	if path == "" && r.URL.Path != "" {
		path = r.URL.Path
	}
	return path
}

func remoteIP(r *http.Request) string {
	if r.RemoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// EnqueueWithContext queues a message and applies any PostHog request context attached to ctx.
// Use this from HTTP handlers with r.Context() when NewRequestContextMiddleware is installed.
func EnqueueWithContext(ctx context.Context, client EnqueueClient, msg Message) error {
	return enqueueWithContext(ctx, client, msg)
}

// EvaluateFlagsWithContext evaluates feature flags and uses the request-scoped distinct ID attached
// to ctx when payload.DistinctId is empty. It does not generate personless IDs: feature flag
// evaluation requires a stable distinct ID and returns ErrNoDistinctID if neither payload nor ctx has one.
func EvaluateFlagsWithContext(ctx context.Context, client Client, payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	if client == nil {
		return noopFeatureFlagEvaluations, fmt.Errorf("posthog: nil client")
	}
	if contextClient, ok := client.(interface {
		EvaluateFlagsWithContext(context.Context, EvaluateFlagsPayload) (*FeatureFlagEvaluations, error)
	}); ok {
		return contextClient.EvaluateFlagsWithContext(ctx, payload)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if payload.DistinctId == "" {
		if requestContext, ok := RequestContextFromContext(ctx); ok {
			payload.DistinctId = requestContext.DistinctId
		}
	}
	return client.EvaluateFlags(payload)
}

func enqueueWithContext(ctx context.Context, client EnqueueClient, msg Message) error {
	if client == nil {
		return fmt.Errorf("posthog: nil enqueue client")
	}
	if contextClient, ok := client.(interface {
		EnqueueWithContext(context.Context, Message) error
	}); ok {
		return contextClient.EnqueueWithContext(ctx, msg)
	}
	return client.Enqueue(msg)
}

func capturePanic(ctx context.Context, client EnqueueClient, panicValue interface{}, statusCode int, panicStack []byte) {
	defer func() {
		// Panic capture runs while the host application is already panicking. Any SDK,
		// callback, or client panic here must not replace the original application panic.
		_ = recover()
	}()

	if client == nil {
		return
	}
	if statusCode < http.StatusBadRequest {
		statusCode = http.StatusInternalServerError
	}

	stacktrace, debugImages := stackTraceFromDebugStack(panicStack)
	exception := Exception{
		Properties: NewProperties().
			Set(propertyResponseStatusCode, statusCode).
			Set(propertyExceptionSource, "panic"),
		ExceptionList: []ExceptionItem{{
			Type:       "panic",
			Value:      fmt.Sprint(panicValue),
			Stacktrace: stacktrace,
		}},
		DebugImages: debugImages,
	}

	_ = enqueueWithContext(ctx, client, exception)
}

func stackTraceFromDebugStack(stack []byte) (*ExceptionStacktrace, []DebugImage) {
	frames := parseDebugStackFrames(stack)
	if len(frames) == 0 {
		// The extractor fallback can emit "native" frames, which need the
		// referenced debug images alongside them.
		extractor := DefaultStackTraceExtractor{InAppDecider: SimpleInAppDecider}
		return extractor.GetStackTrace(4), extractor.GetDebugImages()
	}

	return &ExceptionStacktrace{
		Type:   "raw",
		Frames: frames,
	}, nil
}

func parseDebugStackFrames(stack []byte) []StackFrame {
	lines := strings.Split(string(stack), "\n")
	frames := make([]StackFrame, 0, len(lines)/2)
	include := false

	for i := 1; i+1 < len(lines); i += 2 {
		function := strings.TrimSpace(lines[i])
		if function == "" {
			continue
		}
		if strings.HasPrefix(function, "panic(") {
			include = true
			continue
		}
		if !include {
			continue
		}

		filename, lineNo, ok := parseDebugStackFileLine(lines[i+1])
		if !ok {
			continue
		}

		frames = append(frames, StackFrame{
			Filename:  filename,
			LineNo:    lineNo,
			Function:  function,
			InApp:     SimpleInAppDecider(runtime.Frame{File: filename, Function: function}),
			Synthetic: false,
			Platform:  "go",
		})
	}

	// debug.Stack prints innermost first; reverse to the canonical wire order
	// shared across PostHog SDKs (matching GetStackTrace): outermost (entry
	// point) first, panic site last.
	for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
		frames[i], frames[j] = frames[j], frames[i]
	}

	return frames
}

func parseDebugStackFileLine(line string) (string, int, bool) {
	fileLine := strings.TrimSpace(line)
	if fileLine == "" {
		return "", 0, false
	}
	if idx := strings.Index(fileLine, " +"); idx >= 0 {
		fileLine = fileLine[:idx]
	}
	colon := strings.LastIndex(fileLine, ":")
	if colon <= 0 || colon == len(fileLine)-1 {
		return "", 0, false
	}
	lineNo, err := strconv.Atoi(fileLine[colon+1:])
	if err != nil {
		return "", 0, false
	}
	return fileLine[:colon], lineNo, true
}

type responseStatusWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	flusher     http.Flusher
	hijacker    http.Hijacker
	pusher      http.Pusher
	readerFrom  io.ReaderFrom
}

func newResponseStatusWriter(w http.ResponseWriter) *responseStatusWriter {
	statusWriter := &responseStatusWriter{ResponseWriter: w, statusCode: http.StatusOK}
	if flusher, ok := w.(http.Flusher); ok {
		statusWriter.flusher = flusher
	}
	if hijacker, ok := w.(http.Hijacker); ok {
		statusWriter.hijacker = hijacker
	}
	if pusher, ok := w.(http.Pusher); ok {
		statusWriter.pusher = pusher
	}
	if readerFrom, ok := w.(io.ReaderFrom); ok {
		statusWriter.readerFrom = readerFrom
	}
	return statusWriter
}

func (w *responseStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseStatusWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.statusCode = statusCode
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseStatusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.statusCode = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(data)
}

const (
	responseStatusFlusherFlag = 1 << iota
	responseStatusHijackerFlag
	responseStatusPusherFlag
	responseStatusReaderFromFlag
)

func wrapResponseStatusWriter(w *responseStatusWriter) http.ResponseWriter {
	flags := 0
	if w.flusher != nil {
		flags |= responseStatusFlusherFlag
	}
	if w.hijacker != nil {
		flags |= responseStatusHijackerFlag
	}
	if w.pusher != nil {
		flags |= responseStatusPusherFlag
	}
	if w.readerFrom != nil {
		flags |= responseStatusReaderFromFlag
	}

	switch flags {
	case responseStatusFlusherFlag:
		return &responseStatusWriterFlusher{w}
	case responseStatusHijackerFlag:
		return &responseStatusWriterHijacker{w}
	case responseStatusPusherFlag:
		return &responseStatusWriterPusher{w}
	case responseStatusReaderFromFlag:
		return &responseStatusWriterReaderFrom{w}
	case responseStatusFlusherFlag | responseStatusHijackerFlag:
		return &responseStatusWriterFlusherHijacker{w}
	case responseStatusFlusherFlag | responseStatusPusherFlag:
		return &responseStatusWriterFlusherPusher{w}
	case responseStatusFlusherFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterFlusherReaderFrom{w}
	case responseStatusHijackerFlag | responseStatusPusherFlag:
		return &responseStatusWriterHijackerPusher{w}
	case responseStatusHijackerFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterHijackerReaderFrom{w}
	case responseStatusPusherFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterPusherReaderFrom{w}
	case responseStatusFlusherFlag | responseStatusHijackerFlag | responseStatusPusherFlag:
		return &responseStatusWriterFlusherHijackerPusher{w}
	case responseStatusFlusherFlag | responseStatusHijackerFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterFlusherHijackerReaderFrom{w}
	case responseStatusFlusherFlag | responseStatusPusherFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterFlusherPusherReaderFrom{w}
	case responseStatusHijackerFlag | responseStatusPusherFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterHijackerPusherReaderFrom{w}
	case responseStatusFlusherFlag | responseStatusHijackerFlag | responseStatusPusherFlag | responseStatusReaderFromFlag:
		return &responseStatusWriterFlusherHijackerPusherReaderFrom{w}
	default:
		return w
	}
}

type responseStatusWriterFlusher struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusher) Flush() { w.flusher.Flush() }

type responseStatusWriterHijacker struct{ *responseStatusWriter }

func (w *responseStatusWriterHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}

type responseStatusWriterPusher struct{ *responseStatusWriter }

func (w *responseStatusWriterPusher) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}

type responseStatusWriterReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterFlusherHijacker struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherHijacker) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}

type responseStatusWriterFlusherPusher struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherPusher) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherPusher) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}

type responseStatusWriterFlusherReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherReaderFrom) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterHijackerPusher struct{ *responseStatusWriter }

func (w *responseStatusWriterHijackerPusher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterHijackerPusher) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}

type responseStatusWriterHijackerReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterHijackerReaderFrom) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterHijackerReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterPusherReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterPusherReaderFrom) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}
func (w *responseStatusWriterPusherReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterFlusherHijackerPusher struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherHijackerPusher) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherHijackerPusher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterFlusherHijackerPusher) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}

type responseStatusWriterFlusherHijackerReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherHijackerReaderFrom) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherHijackerReaderFrom) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterFlusherHijackerReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterFlusherPusherReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherPusherReaderFrom) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherPusherReaderFrom) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}
func (w *responseStatusWriterFlusherPusherReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterHijackerPusherReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterHijackerPusherReaderFrom) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterHijackerPusherReaderFrom) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}
func (w *responseStatusWriterHijackerPusherReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}

type responseStatusWriterFlusherHijackerPusherReaderFrom struct{ *responseStatusWriter }

func (w *responseStatusWriterFlusherHijackerPusherReaderFrom) Flush() { w.flusher.Flush() }
func (w *responseStatusWriterFlusherHijackerPusherReaderFrom) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
func (w *responseStatusWriterFlusherHijackerPusherReaderFrom) Push(target string, opts *http.PushOptions) error {
	return w.pusher.Push(target, opts)
}
func (w *responseStatusWriterFlusherHijackerPusherReaderFrom) ReadFrom(reader io.Reader) (int64, error) {
	return w.readerFrom.ReadFrom(reader)
}
