package posthogmcp

import (
	"context"
	"errors"
	"fmt"

	posthog "github.com/posthog/posthog-go/v2"
)

// Option configures Analytics.
type Option func(*config)

type config struct {
	exceptionAutocapture bool
}

func defaultConfig() config {
	return config{exceptionAutocapture: true}
}

// WithExceptionAutocapture controls whether failed tool calls also enqueue a
// PostHog exception. It is enabled by default.
func WithExceptionAutocapture(enabled bool) Option {
	return func(cfg *config) {
		cfg.exceptionAutocapture = enabled
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
// IDs the call leaves empty. Its properties and options sit under
// call.Properties and call.Options, and its properties go through the same
// reserved-key and sanitization rules as call.Properties. They still win over
// the client's Config.DefaultEventProperties and Config.DefaultEventOptions.
func (a *Analytics) CaptureToolCall(ctx context.Context, call ToolCall) error {
	call, enqueueCtx, err := a.withContext(ctx, call)
	if err != nil {
		return err
	}
	messages, err := buildToolCallMessages(call, a.cfg.exceptionAutocapture)
	if err != nil {
		return err
	}
	return a.enqueue(enqueueCtx, messages)
}

// withContext rejects a nil recorder and puts the RequestContext attached to
// ctx under the call. It returns the context to enqueue with, which carries an
// empty request context so the client does not fill the unsanitized values
// again.
func (a *Analytics) withContext(ctx context.Context, call ToolCall) (ToolCall, context.Context, error) {
	if a == nil || a.client == nil {
		return call, nil, errors.New("posthogmcp: nil enqueue client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, ok := posthog.RequestContextFromContext(ctx)
	if !ok {
		return call, ctx, nil
	}
	return withRequestContext(call, requestContext), posthog.WithFreshRequestContext(ctx, posthog.RequestContext{}), nil
}

func (a *Analytics) enqueue(ctx context.Context, messages []namedMessage) error {
	var enqueueErrors []error
	for _, message := range messages {
		if err := posthog.EnqueueWithContext(ctx, a.client, message.message); err != nil {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("posthogmcp: enqueue %s: %w", message.name, err))
		}
	}
	return errors.Join(enqueueErrors...)
}

func withRequestIdentity(call ToolCall, requestContext posthog.RequestContext) ToolCall {
	if call.DistinctID == "" {
		call.DistinctID = requestContext.DistinctId
	}
	if call.SessionID == "" {
		call.SessionID = requestContext.SessionId
	}
	return call
}

// withRequestContext puts the request context under the call.
func withRequestContext(call ToolCall, requestContext posthog.RequestContext) ToolCall {
	call = withRequestIdentity(call, requestContext)
	if len(requestContext.Properties) > 0 {
		properties := posthog.NewProperties().Merge(requestContext.Properties)
		call.Properties = properties.Merge(call.Properties)
	}
	if len(requestContext.Options) > 0 {
		options := make(posthog.Options, len(requestContext.Options)+len(call.Options))
		for name, value := range requestContext.Options {
			options[name] = value
		}
		for name, value := range call.Options {
			options[name] = value
		}
		call.Options = options
	}
	return call
}

type namedMessage struct {
	name    string
	message posthog.Message
}
