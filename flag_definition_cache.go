package posthog

import "context"

// FlagDefinitionCacheData is the set of local evaluation data for a project.
type FlagDefinitionCacheData struct {
	Flags            []FeatureFlag            `json:"flags"`
	GroupTypeMapping map[string]string        `json:"group_type_mapping"`
	Cohorts          map[string]PropertyGroup `json:"cohorts"`
}

// FlagDefinitionCacheProvider shares feature flag definitions between SDK
// instances through an external cache such as Redis.
//
// EXPERIMENTAL: this interface may change in a minor version bump.
type FlagDefinitionCacheProvider interface {
	// GetFlagDefinitions returns the cached flag definitions, or nil when nothing
	// is cached.
	GetFlagDefinitions(ctx context.Context) (*FlagDefinitionCacheData, error)

	// ShouldFetchFlagDefinitions reports whether this instance should fetch
	// definitions from PostHog on this poll.
	ShouldFetchFlagDefinitions(ctx context.Context) (bool, error)

	// OnFlagDefinitionsReceived stores definitions fetched from PostHog.
	OnFlagDefinitionsReceived(ctx context.Context, data FlagDefinitionCacheData) error

	// Shutdown releases any resources held by the provider, such as a lock
	// acquired by ShouldFetchFlagDefinitions.
	Shutdown(ctx context.Context) error
}
