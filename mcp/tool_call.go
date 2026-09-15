package mcp

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

	Intent       string
	IntentSource IntentSource

	Parameters any
	Response   any

	Duration  time.Duration
	IsError   bool
	Error     error
	ErrorType string

	Properties posthog.Properties
	Timestamp  time.Time
}
