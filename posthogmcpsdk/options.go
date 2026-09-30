package posthogmcpsdk

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
)

// Identity describes the PostHog identity associated with an MCP tool call.
type Identity struct {
	DistinctID    string
	Groups        posthog.Groups
	SetProperties posthog.Properties
}

// ToolMetadata provides application-owned metadata about an MCP tool.
type ToolMetadata struct {
	Description string
	Category    string
}

// IdentityResolver resolves the PostHog identity for a tool call.
type IdentityResolver func(context.Context, *mcpsdk.CallToolRequest) (Identity, error)

// ToolMetadataResolver resolves metadata for a tool call.
type ToolMetadataResolver func(context.Context, *mcpsdk.CallToolRequest) (ToolMetadata, error)

// PropertiesResolver resolves additional PostHog properties for a completed
// tool call.
type PropertiesResolver func(
	context.Context,
	*mcpsdk.CallToolRequest,
	*mcpsdk.CallToolResult,
	error,
) (posthog.Properties, error)

// ErrorHandler receives instrumentation failures. Its errors and panics never
// alter the MCP response.
type ErrorHandler func(context.Context, error)

// Option configures MCP server instrumentation.
type Option func(*config)

type config struct {
	recorder          Recorder
	identity          IdentityResolver
	toolMetadata      ToolMetadataResolver
	properties        PropertiesResolver
	errorHandler      ErrorHandler
	captureParameters bool
	captureResponses  bool
	serverName        string
	serverVersion     string
}

func defaultConfig(recorder Recorder) *config {
	return &config{
		recorder:          recorder,
		captureParameters: true,
		captureResponses:  true,
	}
}

// WithIdentity configures application-specific identity resolution.
func WithIdentity(resolver IdentityResolver) Option {
	return func(cfg *config) { cfg.identity = resolver }
}

// WithToolMetadata configures application-specific tool metadata resolution.
func WithToolMetadata(resolver ToolMetadataResolver) Option {
	return func(cfg *config) { cfg.toolMetadata = resolver }
}

// WithCaptureParameters controls capture of raw tool arguments. It is enabled
// by default.
func WithCaptureParameters(enabled bool) Option {
	return func(cfg *config) { cfg.captureParameters = enabled }
}

// WithCaptureResponses controls capture of terminal tool results. It is
// enabled by default.
func WithCaptureResponses(enabled bool) Option {
	return func(cfg *config) { cfg.captureResponses = enabled }
}

// WithProperties configures application-specific event properties.
func WithProperties(resolver PropertiesResolver) Option {
	return func(cfg *config) { cfg.properties = resolver }
}

// WithServerInfo adds the MCP server name and version to captured tool calls.
func WithServerInfo(name, version string) Option {
	return func(cfg *config) {
		cfg.serverName = name
		cfg.serverVersion = version
	}
}

// WithErrorHandler configures instrumentation error reporting. The default is
// a no-op.
func WithErrorHandler(handler ErrorHandler) Option {
	return func(cfg *config) { cfg.errorHandler = handler }
}
