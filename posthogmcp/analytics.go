package posthogmcp

import (
	"context"
	"errors"
	"fmt"

	posthog "github.com/posthog/posthog-go"
)

// Option configures Analytics.
type Option func(*config)

type config struct {
	exceptionAutocapture bool
	captureErrorMessages bool
}

func defaultConfig() config {
	return config{exceptionAutocapture: true, captureErrorMessages: true}
}

// WithExceptionAutocapture controls whether failed tool calls also enqueue a
// PostHog exception. It is enabled by default.
func WithExceptionAutocapture(enabled bool) Option {
	return func(cfg *config) {
		cfg.exceptionAutocapture = enabled
	}
}

// WithCaptureErrorMessages controls whether a failed tool call's error text is
// sent as $mcp_error_message and as the $exception value. Tool errors often
// repeat their arguments, and only credentials are redacted from them. When
// disabled, both carry "Tool <name> returned an error" instead, and the call is
// still recorded as a failure with its error type. It does not cover
// $mcp_response: an IsError result's content, which in posthogmcpsdk includes
// a typed handler's error and go-sdk's validation errors, is a response, so
// also disable the adapter's WithCaptureResponses. It is enabled by default.
func WithCaptureErrorMessages(enabled bool) Option {
	return func(cfg *config) {
		cfg.captureErrorMessages = enabled
	}
}

// Analytics constructs and enqueues canonical PostHog MCP analytics events.
// It does not own the lifecycle of the PostHog client.
type Analytics struct {
	client posthog.EnqueueClient
	cfg    config
}

// New creates an MCP analytics recorder using client.
func New(client posthog.EnqueueClient, opts ...Option) *Analytics {
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &Analytics{client: client, cfg: cfg}
}

// CaptureToolCall validates, transforms, and enqueues one completed MCP tool
// call. When exception autocapture is enabled, all messages are built before
// either is enqueued. Enqueue failures are joined after every configured
// message has been attempted.
//
// A posthog.RequestContext attached to ctx supplies the distinct and session
// IDs the call leaves empty, and its properties sit under call.Properties. They
// go through the same reserved-key and sanitization rules as the call's own.
func (a *Analytics) CaptureToolCall(ctx context.Context, call ToolCall) error {
	call, err := a.withContext(ctx, call)
	if err != nil {
		return err
	}
	messages, err := buildToolCallMessages(call, a.cfg)
	if err != nil {
		return err
	}
	return a.enqueue(messages)
}

// withContext rejects a nil recorder and applies the RequestContext attached
// to ctx.
func (a *Analytics) withContext(ctx context.Context, call ToolCall) (ToolCall, error) {
	if a == nil || a.client == nil {
		return call, errors.New("posthogmcp: nil enqueue client")
	}
	if requestContext, ok := posthog.RequestContextFromContext(ctx); ok {
		call = withRequestContext(call, requestContext)
	}
	return call, nil
}

func (a *Analytics) enqueue(messages []namedMessage) error {
	var enqueueErrors []error
	for _, message := range messages {
		if err := a.client.Enqueue(message.message); err != nil {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("posthogmcp: enqueue %s: %w", message.name, err))
		}
	}
	return errors.Join(enqueueErrors...)
}

func withRequestContext(call ToolCall, requestContext posthog.RequestContext) ToolCall {
	if call.DistinctID == "" {
		call.DistinctID = requestContext.DistinctId
	}
	if call.SessionID == "" {
		call.SessionID = requestContext.SessionId
	}
	if len(requestContext.Properties) > 0 {
		properties := posthog.NewProperties().Merge(requestContext.Properties)
		call.Properties = properties.Merge(call.Properties)
	}
	return call
}

type namedMessage struct {
	name    string
	message posthog.Message
}
