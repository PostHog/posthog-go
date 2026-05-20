package posthog

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const (
	HeaderPostHogDistinctID = "X-PostHog-Distinct-Id"
	HeaderPostHogSessionID  = "X-PostHog-Session-Id"

	propertyProcessPersonProfile = "$process_person_profile"
	propertySessionID            = "$session_id"
	propertyCurrentURL           = "$current_url"
	propertyRequestMethod        = "$request_method"
	propertyRequestPath          = "$request_path"
	propertyUserAgent            = "$user_agent"
	propertyIP                   = "$ip"

	maxRequestContextValueLength = 1000
)

type requestContextKey struct{}

// RequestContext carries request-scoped PostHog metadata that is applied to capture and exception events
// enqueued with EnqueueWithContext.
//
// DistinctId and SessionId usually come from PostHog tracing headers. Properties usually contain safe
// request metadata such as $current_url, $request_method, $request_path, $user_agent, and $ip.
type RequestContext struct {
	DistinctId string
	SessionId  string
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
}

// RequestContextOption customizes request context middleware.
type RequestContextOption func(*RequestContextOptions)

// WithCaptureTracingHeaders configures whether request context middleware reads PostHog tracing headers.
func WithCaptureTracingHeaders(enabled bool) RequestContextOption {
	return func(options *RequestContextOptions) {
		options.CaptureTracingHeaders = enabled
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

		next.ServeHTTP(w, req)
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
