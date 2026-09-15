package mcp

import (
	"errors"
	"fmt"

	posthog "github.com/posthog/posthog-go"
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
func (a *Analytics) CaptureToolCall(call ToolCall) error {
	if a == nil || a.client == nil {
		return errors.New("posthogmcp: nil enqueue client")
	}

	messages, err := buildToolCallMessages(call, a.cfg.exceptionAutocapture)
	if err != nil {
		return err
	}

	var enqueueErrors []error
	for _, message := range messages {
		if err := a.client.Enqueue(message.message); err != nil {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("posthogmcp: enqueue %s: %w", message.name, err))
		}
	}
	return errors.Join(enqueueErrors...)
}

type namedMessage struct {
	name    string
	message posthog.Message
}
