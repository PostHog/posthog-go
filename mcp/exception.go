// Event-size pruning is adapted from AgentCat-derived MCP analytics code in
// PostHog/posthog-js. See THIRD_PARTY_NOTICES.md.

package mcp

import (
	"errors"

	posthog "github.com/posthog/posthog-go"
)

func (p preparedToolCall) buildCapture() (posthog.Capture, error) {
	base := p.baseProperties()

	build := func(depth int, includeResponse, includeParameters, includeCustom, includeSet bool) posthog.Capture {
		var properties posthog.Properties
		if includeCustom {
			properties = mergeProperties(base, p.custom)
		} else {
			properties = mergeProperties(base, nil)
		}
		if includeResponse {
			if value, ok := properties[propertyResponse]; ok {
				properties[propertyResponse] = truncateNested(value, depth)
			}
		} else {
			delete(properties, propertyResponse)
		}
		if includeParameters {
			if value, ok := properties[propertyParameters]; ok {
				properties[propertyParameters] = truncateNested(value, depth)
			}
		} else {
			delete(properties, propertyParameters)
		}
		applyIdentityProperties(properties, p, includeSet)
		return posthog.Capture{
			DistinctId: p.distinctID,
			Event:      eventToolCall,
			Timestamp:  p.call.Timestamp,
			Properties: properties,
			Groups:     p.groups,
		}
	}

	attempts := []posthog.Capture{build(maxDepth, true, true, true, true)}
	for depth := maxDepth - 1; depth >= 1; depth-- {
		attempts = append(attempts, build(depth, true, true, true, true))
	}
	attempts = append(attempts,
		build(1, false, true, true, true),
		build(1, false, false, true, true),
		build(1, false, false, false, true),
		build(1, false, false, false, false),
	)
	for _, capture := range attempts {
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

func (p preparedToolCall) buildException() (posthog.Exception, error) {
	handled := true
	synthetic := true
	item := posthog.ExceptionItem{
		Type:  p.errorType,
		Value: p.errorMessage,
		Mechanism: &posthog.ExceptionMechanism{
			Handled:   &handled,
			Synthetic: &synthetic,
		},
	}

	base := posthog.NewProperties()
	setStringProperty(base, propertySessionID, p.call.SessionID)
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
		var properties posthog.Properties
		if includeCustom {
			properties = mergeProperties(base, p.custom)
		} else {
			properties = mergeProperties(base, nil)
		}
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

	for _, exception := range []posthog.Exception{build(true), build(false)} {
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
