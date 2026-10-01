// Event-size pruning is adapted from AgentCat-derived MCP analytics code in
// PostHog/posthog-js. See THIRD_PARTY_NOTICES.md.

package posthogmcp

import (
	"errors"

	posthog "github.com/posthog/posthog-go"
)

// captureStage says what a $mcp_tool_call keeps: nested values are cut to depth
// levels, and each flag keeps one optional group of properties.
type captureStage struct {
	depth      int
	response   bool
	parameters bool
	custom     bool
	set        bool
}

// Each optional group dropped by a stage stays dropped in the stages after it.
var (
	withoutResponse = captureStage{depth: 1, parameters: true, custom: true, set: true}
	withoutPayloads = captureStage{depth: 1, custom: true, set: true}
	withoutCustom   = captureStage{depth: 1, set: true}
	requiredOnly    = captureStage{depth: 1}
)

// captureStages lists what to try, richest first: full content at every depth
// from maxDepth down to 1, then each optional group dropped in turn.
var captureStages = func() []captureStage {
	stages := make([]captureStage, 0, maxDepth+4)
	for depth := maxDepth; depth >= 1; depth-- {
		stages = append(stages, captureStage{depth: depth, response: true, parameters: true, custom: true, set: true})
	}
	return append(stages, withoutResponse, withoutPayloads, withoutCustom, requiredOnly)
}()

func (p preparedToolCall) buildCapture() (posthog.Capture, error) {
	base := p.baseProperties()
	for _, stage := range captureStages {
		capture := p.captureAt(base, stage)
		size, err := messageSize(capture)
		if err != nil {
			return posthog.Capture{}, err
		}
		if size <= maxEventBytes {
			return capture, nil
		}
	}
	return posthog.Capture{}, errors.New("posthogmcp: required tool-call event exceeds 102400 bytes")
}

func (p preparedToolCall) captureAt(base posthog.Properties, stage captureStage) posthog.Capture {
	var custom posthog.Properties
	if stage.custom {
		custom = p.custom
	}
	properties := mergeProperties(base, custom)
	limitProperty(properties, propertyResponse, stage.response, stage.depth)
	limitProperty(properties, propertyParameters, stage.parameters, stage.depth)
	applyIdentityProperties(properties, p, stage.set)
	return posthog.Capture{
		DistinctId: p.distinctID,
		Event:      eventToolCall,
		Timestamp:  p.call.Timestamp,
		Properties: properties,
		Groups:     p.groups,
	}
}

func limitProperty(properties posthog.Properties, key string, keep bool, depth int) {
	value, ok := properties[key]
	switch {
	case !ok:
	case keep:
		properties[key] = truncateNested(value, depth)
	default:
		delete(properties, key)
	}
}

func (p preparedToolCall) buildException() (posthog.Exception, error) {
	handled := true
	synthetic := true
	item := posthog.ExceptionItem{
		Type:  p.exceptionType,
		Value: p.errorMessage,
		Mechanism: &posthog.ExceptionMechanism{
			Handled:   &handled,
			Synthetic: &synthetic,
		},
	}

	base := posthog.NewProperties().Set(propertyExceptionLevel, "error")
	setStringProperty(base, propertySessionID, p.sessionID)
	setStringProperty(base, propertyConversationID, p.conversationID)
	setStringProperty(base, propertyResourceName, p.toolName)
	setStringProperty(base, propertyToolName, p.toolName)
	setStringProperty(base, propertyToolDescription, truncateUTF8(p.call.ToolDescription, maxStringBytes))
	setStringProperty(base, propertyToolCategory, truncateUTF8(p.call.ToolCategory, maxMetadataBytes))
	setStringProperty(base, propertyServerName, truncateUTF8(p.call.ServerName, maxMetadataBytes))
	setStringProperty(base, propertyServerVersion, truncateUTF8(p.call.ServerVersion, maxMetadataBytes))
	setStringProperty(base, propertyClientName, truncateUTF8(p.call.ClientName, maxMetadataBytes))
	setStringProperty(base, propertyClientVersion, truncateUTF8(p.call.ClientVersion, maxMetadataBytes))
	setStringProperty(base, propertyProtocolVersion, truncateUTF8(p.call.ProtocolVersion, maxMetadataBytes))

	build := func(includeCustom bool) posthog.Exception {
		var custom posthog.Properties
		if includeCustom {
			custom = p.custom
		}
		properties := mergeProperties(base, custom)
		applyIdentityProperties(properties, p, false)
		return posthog.Exception{
			DistinctId: p.distinctID,
			Timestamp:  p.call.Timestamp,
			Properties: properties,
			ExceptionList: []posthog.ExceptionItem{
				item,
			},
		}
	}

	for _, includeCustom := range []bool{true, false} {
		exception := build(includeCustom)
		size, err := messageSize(exception)
		if err != nil {
			return posthog.Exception{}, err
		}
		if size <= maxEventBytes {
			return exception, nil
		}
	}
	return posthog.Exception{}, errors.New("posthogmcp: required MCP exception exceeds 102400 bytes")
}
