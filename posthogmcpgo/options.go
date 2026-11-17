package posthogmcpgo

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

// Identity describes the PostHog identity associated with an MCP tool call.
type Identity struct {
	DistinctID    string
	Groups        posthog.Groups
	SetProperties posthog.Properties
}

// IdentityResolver resolves the PostHog identity for a tool call. It receives
// the request as the client sent it, including the analytics arguments.
type IdentityResolver func(context.Context, mcp.CallToolRequest) (Identity, error)

// PropertiesResolver resolves additional PostHog properties for a completed
// tool call.
type PropertiesResolver func(
	context.Context,
	mcp.CallToolRequest,
	*mcp.CallToolResult,
	error,
) (posthog.Properties, error)

// EventOptionsResolver resolves PostHog capture options, such as
// process_person_profile, for a completed tool call.
type EventOptionsResolver func(
	context.Context,
	mcp.CallToolRequest,
	*mcp.CallToolResult,
	error,
) (posthog.Options, error)

// ErrorHandler receives instrumentation failures. Its errors and panics never
// alter the MCP response.
type ErrorHandler func(context.Context, error)

// Option configures MCP server instrumentation.
type Option func(*config)

type config struct {
	analytics         *posthogmcp.Analytics
	identity          IdentityResolver
	properties        PropertiesResolver
	eventOptions      EventOptionsResolver
	errorHandler      ErrorHandler
	captureParameters bool
	captureResponses  bool
	contextParameter  bool
	captureModel      bool
	serverName        string
	serverVersion     string
}

func defaultConfig(analytics *posthogmcp.Analytics) *config {
	return &config{
		analytics:         analytics,
		captureParameters: true,
		captureResponses:  true,
		contextParameter:  true,
		captureModel:      true,
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

// WithCaptureResponses controls capture of tool results as $mcp_response. It
// is enabled by default. Disabling it does not cover failures: the text of an
// IsError result is still sent as $mcp_error_message and in a $exception
// event, unless the recorder is created with
// posthogmcp.WithCaptureErrorMessages(false).
func WithCaptureResponses(enabled bool) Option {
	return func(cfg *config) { cfg.captureResponses = enabled }
}

// WithContextParameter controls whether tools are advertised with a required
// context argument in which the agent states why it is calling the tool,
// captured as the event's intent. The argument is removed before the tool's
// handler sees the call. Tools that declare their own context argument keep
// it, and its value is captured as tool data in $mcp_parameters, not as the
// intent. It is enabled by default.
func WithContextParameter(enabled bool) Option {
	return func(cfg *config) { cfg.contextParameter = enabled }
}

// WithCaptureModel controls capture of the model that made each call. The
// model comes from the client's own request metadata when it names one, else
// from an llm_model argument the agent fills in, advertised as required and
// removed before the tool's handler sees the call, like context. Tools that
// declare their own llm_model argument keep it, and its value is not read as
// the model. It is enabled by default.
func WithCaptureModel(enabled bool) Option {
	return func(cfg *config) { cfg.captureModel = enabled }
}

// WithProperties configures application-specific event properties.
func WithProperties(resolver PropertiesResolver) Option {
	return func(cfg *config) { cfg.properties = resolver }
}

// WithEventOptions configures application-specific capture options for
// $mcp_tool_call and its $exception, as posthogmcp.ToolCall.EventOptions. A
// call without a distinct ID still gets process_person_profile set to false.
// An error or panic is reported to the ErrorHandler and the call is captured
// without these options.
func WithEventOptions(resolver EventOptionsResolver) Option {
	return func(cfg *config) { cfg.eventOptions = resolver }
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

func (cfg *config) injectedArguments() []analyticsArgument {
	var arguments []analyticsArgument
	if cfg.contextParameter {
		arguments = append(arguments, contextParameter)
	}
	if cfg.captureModel {
		arguments = append(arguments, modelParameter)
	}
	return arguments
}
