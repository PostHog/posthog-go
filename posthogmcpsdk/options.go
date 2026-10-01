package posthogmcpsdk

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
)

// Identity describes the PostHog identity associated with an MCP tool call.
type Identity struct {
	DistinctID    string
	Groups        posthog.Groups
	SetProperties posthog.Properties
}

// IdentityResolver resolves the PostHog identity for a tool call.
type IdentityResolver func(context.Context, *mcpsdk.CallToolRequest) (Identity, error)

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
	analytics         *posthogmcp.Analytics
	identity          IdentityResolver
	properties        PropertiesResolver
	errorHandler      ErrorHandler
	captureParameters bool
	captureResponses  bool
	contextParameter  bool
	serverName        string
	serverVersion     string
	now               func() time.Time
}

func defaultConfig(analytics *posthogmcp.Analytics) *config {
	return &config{
		analytics:         analytics,
		captureParameters: true,
		captureResponses:  true,
		contextParameter:  true,
		now:               time.Now,
	}
}

// WithIdentity configures application-specific identity resolution.
func WithIdentity(resolver IdentityResolver) Option {
	return func(cfg *config) { cfg.identity = resolver }
}

// WithCaptureParameters controls capture of raw tool arguments. It is enabled
// by default.
func WithCaptureParameters(enabled bool) Option {
	return func(cfg *config) { cfg.captureParameters = enabled }
}

// WithCaptureResponses controls capture of terminal tool results as
// $mcp_response. It is enabled by default. Disabling it does not cover
// failures: the text of an IsError result is still sent as $mcp_error_message
// and in a $exception event, as in the Python SDK.
func WithCaptureResponses(enabled bool) Option {
	return func(cfg *config) { cfg.captureResponses = enabled }
}

// WithContextParameter controls whether tools are advertised with a required
// context argument in which the agent states why it is calling the tool,
// captured as the event's intent. The argument is removed before the tool's
// handler and input validation see the call. Tools that declare their own
// context argument keep it, and their value is still captured as the intent.
// It is enabled by default.
func WithContextParameter(enabled bool) Option {
	return func(cfg *config) { cfg.contextParameter = enabled }
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
