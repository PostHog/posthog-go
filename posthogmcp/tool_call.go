package posthogmcp

import (
	"time"

	posthog "github.com/posthog/posthog-go"
)

// IntentSource identifies how an MCP tool-call intent was obtained.
type IntentSource string

const (
	// IntentSourceContextParameter means the MCP client supplied the intent in a
	// tool context parameter.
	IntentSourceContextParameter IntentSource = "context_parameter"
	// IntentSourceInferred means the host application inferred the intent.
	IntentSourceInferred IntentSource = "inferred"
)

// ModelSource identifies how an MCP tool-call LLM model name was obtained.
type ModelSource string

const (
	// ModelSourceClientMetadata means the MCP client's own metadata named the
	// model.
	ModelSourceClientMetadata ModelSource = "client_metadata"
	// ModelSourceSelfReported means the calling agent reported the model.
	ModelSourceSelfReported ModelSource = "self_reported"
)

// ToolCall describes one completed MCP tool invocation.
type ToolCall struct {
	// ToolName is required and not blank. It is captured as $mcp_tool_name and
	// $mcp_resource_name.
	ToolName string
	// ToolDescription is captured as $mcp_tool_description.
	ToolDescription string
	// ToolCategory is captured as $mcp_tool_category.
	ToolCategory string

	// DistinctID is the event's distinct ID. When empty, the ID falls back to
	// the RequestContext's, then SessionID, then "anonymous", and no person
	// profile is processed.
	DistinctID string
	// SessionID is captured as $session_id and is the distinct ID fallback. A
	// valid ConversationID replaces it with the session derived from the handle.
	SessionID string
	// Groups is captured as $groups on both events.
	Groups posthog.Groups
	// SetProperties is captured as $set on $mcp_tool_call. It needs an explicit
	// DistinctID and is dropped without one.
	SetProperties posthog.Properties

	// ServerName is captured as $mcp_server_name on both events.
	ServerName string
	// ServerVersion is captured as $mcp_server_version on both events.
	ServerVersion string
	// ClientName is captured as $mcp_client_name on both events.
	ClientName string
	// ClientVersion is captured as $mcp_client_version on both events.
	ClientVersion string
	// ProtocolVersion is captured as $mcp_protocol_version on both events.
	ProtocolVersion string

	// ConversationID is captured as $mcp_conversation_id on both events, since it
	// joins tool calls into a conversation. Only a UUIDv7-shaped value is kept,
	// lowercased, and anything else is dropped.
	ConversationID string
	// ClientUserAgent is the HTTP User-Agent header of the request, captured as
	// $mcp_client_user_agent on $mcp_tool_call with credentials redacted.
	ClientUserAgent string
	// VendorClient is the client vendor the transport reported, captured as
	// $mcp_vendor_client on $mcp_tool_call.
	VendorClient string
	// LLMModel is the model that made the call, captured as $mcp_llm_model on
	// $mcp_tool_call. A blank value or "unknown" is not recorded.
	LLMModel string
	// LLMModelSource says where LLMModel came from and defaults to
	// ModelSourceSelfReported. It is only recorded, and only validated, with a model.
	LLMModelSource ModelSource

	// Intent is the agent's stated reason for the call. When it arrives as a
	// tool argument, remove that argument from Parameters: Parameters only has
	// credentials redacted, while Intent also has personal data redacted.
	Intent string
	// IntentSource says how Intent was obtained and defaults to
	// IntentSourceContextParameter. It is only recorded with an Intent.
	IntentSource IntentSource

	// Parameters is captured as $mcp_parameters as given, like the manual
	// capture APIs in the Python and TypeScript SDKs. Automatic
	// instrumentation passes the JSON-RPC request here, as
	// {"request": {"method": "tools/call", "params": {"name": ..., "arguments": ...}}}.
	Parameters any
	// Response is captured as $mcp_response with credentials and media content
	// redacted. A response too large to process is replaced by a marker.
	Response any

	// Duration is captured as $mcp_duration_ms and must not be negative.
	Duration time.Duration
	// IsError marks a failed call that has no Go error, such as an MCP result
	// with isError set. A non-nil Error implies IsError.
	IsError bool
	// Error is the failure, if any. When set, it is also captured as a
	// $exception unless exception autocapture is disabled.
	Error error
	// ErrorType is a coarse category captured as $mcp_error_type, such as
	// "validation" or "timeout". When empty it is the Go type of Error, such as
	// fs.PathError, else "Error". The $exception event always carries the Go
	// type of Error.
	ErrorType string

	// Properties adds custom event metadata. $mcp_* and identity control keys
	// are reserved; use the corresponding ToolCall fields instead.
	Properties posthog.Properties
	// Timestamp is the time of the event. When zero, the PostHog client stamps
	// the time it enqueues the event.
	Timestamp time.Time
}
