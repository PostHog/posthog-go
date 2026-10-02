package posthogmcpsdk

import (
	"context"
	"strings"
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
	captureModel      bool
	conversationID    bool
	// missingCapabilityTool is the name of the virtual tool, empty when it is off.
	missingCapabilityTool string
	serverName            string
	serverVersion         string
	now                   func() time.Time
}

func defaultConfig(analytics *posthogmcp.Analytics) *config {
	return &config{
		analytics:         analytics,
		captureParameters: true,
		captureResponses:  true,
		contextParameter:  true,
		captureModel:      true,
		conversationID:    true,
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

// WithCaptureModel controls capture of the model that made each call. The
// model comes from the client's own request metadata when it names one, else
// from an llm_model argument the agent fills in, advertised as required and
// removed before the tool's handler and input validation see the call, like
// context. Tools that declare their own llm_model argument keep it, and its
// value is not read as the model. It is enabled by default.
func WithCaptureModel(enabled bool) Option {
	return func(cfg *config) { cfg.captureModel = enabled }
}

// WithConversationID controls conversation anchoring, which keeps the calls
// of one agent conversation in one $session_id where the transport carries no
// session, as on stateless HTTP. Tools are advertised with an optional
// conversation_id argument. A call that arrives with neither a transport
// session nor a valid handle gets a new UUIDv7 handle, appended to its result
// as a {"conversation_id": ...} text block, and the agent echoes it on later
// calls. A valid handle sets $mcp_conversation_id and the $session_id derived
// from it, also over a transport session. The handle is mirrored into
// structuredContent under _mcp_instructions for tools whose output schema can
// declare it. The argument is removed before the tool's handler and input
// validation see the call, and tools that declare their own conversation_id
// keep it. It is enabled by default.
func WithConversationID(enabled bool) Option {
	return func(cfg *config) { cfg.conversationID = enabled }
}

// WithMissingCapabilityTool advertises a virtual tool, named name (trimmed) or
// "get_more_tools" when name is blank, that agents call to report a capability
// the server lacks. The report is the tool's required context argument,
// captured as $mcp_intent on a $mcp_missing_capability event, which is not a
// $mcp_tool_call. The middleware answers the call itself, and the server's
// handlers never see it. The tool also gets the llm_model and conversation_id
// arguments the configuration enables. It is not advertised, and its name is
// the server's, when the server registers a tool of that name. It is off by
// default.
func WithMissingCapabilityTool(name string) Option {
	return func(cfg *config) {
		cfg.missingCapabilityTool = strings.TrimSpace(name)
		if cfg.missingCapabilityTool == "" {
			cfg.missingCapabilityTool = defaultMissingCapabilityTool
		}
	}
}

// injectedArguments are the analytics arguments the configuration advertises.
func (cfg *config) injectedArguments() []analyticsArgument {
	var arguments []analyticsArgument
	if cfg.contextParameter {
		arguments = append(arguments, contextParameter)
	}
	if cfg.captureModel {
		arguments = append(arguments, modelParameter)
	}
	if cfg.conversationID {
		arguments = append(arguments, conversationParameter)
	}
	return arguments
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
