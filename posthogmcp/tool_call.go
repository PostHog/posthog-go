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

// ToolCall describes one completed MCP tool invocation.
type ToolCall struct {
	ToolName        string
	ToolDescription string
	ToolCategory    string

	DistinctID    string
	SessionID     string
	Groups        posthog.Groups
	SetProperties posthog.Properties

	ServerName      string
	ServerVersion   string
	ClientName      string
	ClientVersion   string
	ProtocolVersion string

	// Intent is the agent's stated reason for the call. When it arrives as a
	// tool argument, remove that argument from Parameters: Parameters only has
	// credentials redacted, while Intent also has personal data redacted.
	Intent       string
	IntentSource IntentSource

	// Parameters is captured as $mcp_parameters as given, like the manual
	// capture APIs in the Python and TypeScript SDKs. Automatic
	// instrumentation passes the JSON-RPC request here, as
	// {"request": {"method": "tools/call", "params": {"name": ..., "arguments": ...}}}.
	Parameters any
	Response   any

	Duration time.Duration
	// IsError marks a failed call that has no Go error, such as an MCP result
	// with isError set. A non-nil Error implies IsError.
	IsError bool
	// Error is the failure, if any. When set, it is also captured as a
	// $exception unless exception autocapture is disabled.
	Error     error
	ErrorType string

	// Properties adds custom event metadata. $mcp_* and identity control keys
	// are reserved; use the corresponding ToolCall fields instead.
	Properties posthog.Properties
	Timestamp  time.Time
}
