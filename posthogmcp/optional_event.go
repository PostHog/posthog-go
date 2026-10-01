package posthogmcp

import (
	"context"
	"errors"
	"time"

	posthog "github.com/posthog/posthog-go"
)

// maxInputRequestMethods bounds $mcp_input_request_methods.
const maxInputRequestMethods = 100

// EventContext is what $mcp_unknown_tool and $mcp_input_required carry besides
// their own fields. Each field is captured, redacted, and bounded like the
// ToolCall field of the same name.
type EventContext struct {
	// DistinctID falls back to the RequestContext's, then SessionID, then "anonymous".
	DistinctID string
	// SessionID is captured as $session_id. A valid ConversationID replaces it.
	SessionID string
	// Groups is captured as $groups.
	Groups posthog.Groups
	// SetProperties is captured as $set, and needs an explicit DistinctID.
	SetProperties posthog.Properties

	ServerName      string
	ServerVersion   string
	ClientName      string
	ClientVersion   string
	ProtocolVersion string
	ConversationID  string
	ClientUserAgent string
	VendorClient    string

	// Properties adds custom event metadata. $mcp_* and identity control keys
	// are reserved.
	Properties posthog.Properties
	// Timestamp is the time of the event. When zero, the PostHog client stamps
	// the time it enqueues the event.
	Timestamp time.Time
}

func (c EventContext) toolCall(toolName string) ToolCall {
	return ToolCall{
		ToolName:        toolName,
		DistinctID:      c.DistinctID,
		SessionID:       c.SessionID,
		Groups:          c.Groups,
		SetProperties:   c.SetProperties,
		ServerName:      c.ServerName,
		ServerVersion:   c.ServerVersion,
		ClientName:      c.ClientName,
		ClientVersion:   c.ClientVersion,
		ProtocolVersion: c.ProtocolVersion,
		ConversationID:  c.ConversationID,
		ClientUserAgent: c.ClientUserAgent,
		VendorClient:    c.VendorClient,
		Properties:      c.Properties,
		Timestamp:       c.Timestamp,
	}
}

// UnknownTool describes a call that named a tool the server has not
// registered.
type UnknownTool struct {
	EventContext
	// ToolName is the name the agent sent, captured as $mcp_tool_name and
	// redacted as free text. It is required and not blank.
	ToolName string
}

// InputRequired describes one input_required round a client received. The
// call is not complete: the retry that completes it is a ToolCall.
type InputRequired struct {
	EventContext
	// ToolName is required and not blank.
	ToolName string
	// Duration is how long this round took, and must not be negative.
	Duration time.Duration
	// Methods is the method of each input request, in the order of the
	// requests' keys and duplicates kept, captured as
	// $mcp_input_request_methods. Never pass the requests or the answers.
	Methods []string
}

// CaptureUnknownTool validates, transforms, and enqueues one
// $mcp_unknown_tool event. It is not a tool call, and enqueues no
// $mcp_tool_call or $exception.
func (a *Analytics) CaptureUnknownTool(ctx context.Context, event UnknownTool) error {
	return a.captureOptional(ctx, event.toolCall(event.ToolName), func(p preparedToolCall) (posthog.Capture, error) {
		p.toolName = truncateUTF8(sanitizeFreeText(truncateUTF8(event.ToolName, 2*maxResourceNameBytes)), maxResourceNameBytes)
		return p.buildOptionalEvent(eventUnknownTool, posthog.NewProperties())
	})
}

// CaptureInputRequired validates, transforms, and enqueues one
// $mcp_input_required event, carrying the methods of the round's requests and
// never their content.
func (a *Analytics) CaptureInputRequired(ctx context.Context, event InputRequired) error {
	call := event.toolCall(event.ToolName)
	call.Duration = event.Duration
	methods := make([]string, 0, min(len(event.Methods), maxInputRequestMethods))
	for _, method := range event.Methods[:min(len(event.Methods), maxInputRequestMethods)] {
		methods = append(methods, truncateUTF8(method, maxMetadataBytes))
	}
	return a.captureOptional(ctx, call, func(p preparedToolCall) (posthog.Capture, error) {
		specific := posthog.NewProperties().
			Set(propertyDurationMS, float64(event.Duration)/float64(time.Millisecond)).
			Set(propertyInputRequestMethods, methods)
		return p.buildOptionalEvent(eventInputRequired, specific)
	})
}

func (a *Analytics) captureOptional(ctx context.Context, call ToolCall, build func(preparedToolCall) (posthog.Capture, error)) error {
	call, err := a.withContext(ctx, call)
	if err != nil {
		return err
	}
	prepared, err := prepareToolCall(call)
	if err != nil {
		return err
	}
	capture, err := build(prepared)
	if err != nil {
		return err
	}
	return a.enqueue([]namedMessage{{name: capture.Event, message: capture}})
}

// buildOptionalEvent builds an event of the tool name, the identity every MCP
// event carries, and specific. Custom properties are dropped when the event is
// too large.
func (p preparedToolCall) buildOptionalEvent(event string, specific posthog.Properties) (posthog.Capture, error) {
	base := mergeProperties(specific, posthog.Properties{propertyToolName: p.toolName})
	p.setIdentityProperties(base)
	for _, custom := range []posthog.Properties{p.custom, nil} {
		properties := mergeProperties(base, custom)
		applyIdentityProperties(properties, p, true)
		capture := posthog.Capture{
			DistinctId: p.distinctID,
			Event:      event,
			Timestamp:  p.call.Timestamp,
			Properties: properties,
			Groups:     p.groups,
		}
		size, err := messageSize(capture)
		if err != nil {
			return posthog.Capture{}, err
		}
		if size <= maxEventBytes {
			return capture, nil
		}
	}
	return posthog.Capture{}, errors.New("posthogmcp: required " + event + " event exceeds 102400 bytes")
}
