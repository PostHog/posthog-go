package posthog

import (
	"context"
	"encoding/json"
)

// FlagDefinitionCacheProvider shares feature flag definitions between SDK
// instances through an external cache such as Redis. Definition data is opaque:
// providers store and return it unchanged; the SDK validates and interprets it.
// Implementations should honor context cancellation so polling and resource
// cleanup can finish promptly.
//
// EXPERIMENTAL: this interface may change in a minor version bump.
type FlagDefinitionCacheProvider interface {
	// GetFlagDefinitions returns the cached JSON, or nil when nothing is cached.
	GetFlagDefinitions(ctx context.Context) (json.RawMessage, error)

	// ShouldFetchFlagDefinitions reports whether this instance should fetch
	// definitions from PostHog on this poll.
	ShouldFetchFlagDefinitions(ctx context.Context) (bool, error)

	// OnFlagDefinitionsReceived stores the JSON fetched from PostHog unchanged.
	OnFlagDefinitionsReceived(ctx context.Context, data json.RawMessage) error

	// Shutdown releases any resources held by the provider, such as a lock
	// acquired by ShouldFetchFlagDefinitions. It runs after active provider calls
	// finish. If client shutdown times out, cleanup may complete after Close or
	// CloseWithContext returns.
	Shutdown(ctx context.Context) error
}
