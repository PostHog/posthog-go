package mcp

import (
	"errors"
	"fmt"
	"strings"
	"time"

	posthog "github.com/posthog/posthog-go"
)

type preparedToolCall struct {
	call          ToolCall
	distinctID    string
	explicitID    bool
	toolName      string
	intent        string
	intentSource  IntentSource
	errorType     string
	errorMessage  string
	parameters    any
	response      any
	groups        posthog.Groups
	setProperties posthog.Properties
	custom        posthog.Properties
}

func buildToolCallMessages(call ToolCall, exceptionAutocapture bool) ([]namedMessage, error) {
	prepared, err := prepareToolCall(call)
	if err != nil {
		return nil, err
	}

	capture, err := prepared.buildCapture()
	if err != nil {
		return nil, err
	}
	messages := []namedMessage{{name: eventToolCall, message: capture}}

	if call.IsError && exceptionAutocapture {
		exception, err := prepared.buildException()
		if err != nil {
			return nil, err
		}
		messages = append(messages, namedMessage{name: "$exception", message: exception})
	}
	return messages, nil
}

func prepareToolCall(call ToolCall) (preparedToolCall, error) {
	if strings.TrimSpace(call.ToolName) == "" {
		return preparedToolCall{}, errors.New("posthogmcp: ToolName must not be blank")
	}
	if call.Duration < 0 {
		return preparedToolCall{}, errors.New("posthogmcp: Duration must not be negative")
	}
	if call.IntentSource != "" &&
		call.IntentSource != IntentSourceContextParameter &&
		call.IntentSource != IntentSourceInferred {
		return preparedToolCall{}, errors.New("posthogmcp: invalid IntentSource")
	}

	prepared := preparedToolCall{
		call:       call,
		explicitID: call.DistinctID != "",
		toolName:   truncateUTF8(call.ToolName, maxResourceNameBytes),
	}
	switch {
	case call.DistinctID != "":
		prepared.distinctID = call.DistinctID
	case call.SessionID != "":
		prepared.distinctID = call.SessionID
	default:
		prepared.distinctID = "anonymous"
	}

	intent := strings.TrimSpace(call.Intent)
	if intent != "" {
		prepared.intent = truncateUTF8(sanitizeString(intent), maxIntentBytes)
		prepared.intentSource = call.IntentSource
		if prepared.intentSource == "" {
			prepared.intentSource = IntentSourceContextParameter
		}
	}

	var err error
	prepared.parameters, err = prepareValue("Parameters", call.Parameters, false)
	if err != nil {
		return preparedToolCall{}, err
	}
	prepared.response, err = prepareValue("Response", call.Response, true)
	if err != nil {
		return preparedToolCall{}, err
	}
	prepared.groups, err = prepareGroups(call.Groups)
	if err != nil {
		return preparedToolCall{}, err
	}
	prepared.setProperties, err = prepareProperties("SetProperties", call.SetProperties)
	if err != nil {
		return preparedToolCall{}, err
	}
	prepared.custom, err = prepareProperties("Properties", call.Properties)
	if err != nil {
		return preparedToolCall{}, err
	}
	removeIdentityControlProperties(prepared.custom)

	if call.IsError {
		prepared.errorType = strings.TrimSpace(call.ErrorType)
		if prepared.errorType == "" {
			prepared.errorType = "Error"
		}
		prepared.errorType = truncateUTF8(prepared.errorType, maxMetadataBytes)

		message := fmt.Sprintf("Tool %s returned an error", prepared.toolName)
		if call.Error != nil {
			message, err = safeErrorMessage(call.Error)
			if err != nil {
				return preparedToolCall{}, err
			}
		}
		prepared.errorMessage = truncateUTF8(sanitizeString(message), maxErrorMessageBytes)
	}

	return prepared, nil
}

func prepareValue(field string, value any, response bool) (any, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := normalizePayload(field, value)
	if err != nil {
		return nil, err
	}
	if response {
		normalized = sanitizeResponse(normalized)
	} else {
		normalized = sanitizeCapturedValue(normalized)
	}
	return truncateValue(normalized), nil
}

func prepareProperties(field string, properties posthog.Properties) (posthog.Properties, error) {
	if len(properties) == 0 {
		return nil, nil
	}
	normalized, err := normalizePayload(field, properties)
	if err != nil {
		return nil, err
	}
	value, ok := truncateValue(sanitizeCapturedValue(normalized)).(map[string]any)
	if !ok {
		return nil, errors.New("posthogmcp: normalized properties must be an object")
	}
	return posthog.Properties(value), nil
}

func prepareGroups(groups posthog.Groups) (posthog.Groups, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	normalized, err := normalizePayload("Groups", groups)
	if err != nil {
		return nil, err
	}
	value, ok := truncateValue(sanitizeCapturedValue(normalized)).(map[string]any)
	if !ok {
		return nil, errors.New("posthogmcp: normalized groups must be an object")
	}
	return posthog.Groups(value), nil
}

func safeErrorMessage(err error) (message string, resultErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			message = ""
			resultErr = errors.New("posthogmcp: Error method panicked")
		}
	}()
	return err.Error(), nil
}

func removeIdentityControlProperties(properties posthog.Properties) {
	delete(properties, propertyGroups)
	delete(properties, propertySet)
	delete(properties, propertyProcessProfile)
}

func (p preparedToolCall) baseProperties() posthog.Properties {
	properties := posthog.NewProperties().
		Set(propertySource, analyticsSource).
		Set(propertyResourceName, p.toolName).
		Set(propertyToolName, p.toolName).
		Set(propertyDurationMS, float64(p.call.Duration)/float64(time.Millisecond)).
		Set(propertyIsError, p.call.IsError)

	setStringProperty(properties, propertyToolDescription, truncateUTF8(p.call.ToolDescription, maxStringBytes))
	setStringProperty(properties, propertyToolCategory, truncateUTF8(p.call.ToolCategory, maxMetadataBytes))
	setStringProperty(properties, propertySessionID, p.call.SessionID)
	setStringProperty(properties, propertyServerName, truncateUTF8(p.call.ServerName, maxMetadataBytes))
	setStringProperty(properties, propertyServerVersion, truncateUTF8(p.call.ServerVersion, maxMetadataBytes))
	setStringProperty(properties, propertyClientName, truncateUTF8(p.call.ClientName, maxMetadataBytes))
	setStringProperty(properties, propertyClientVersion, truncateUTF8(p.call.ClientVersion, maxMetadataBytes))
	setStringProperty(properties, propertyProtocolVersion, truncateUTF8(p.call.ProtocolVersion, maxMetadataBytes))
	setStringProperty(properties, propertyIntent, p.intent)
	if p.intent != "" {
		properties[propertyIntentSource] = string(p.intentSource)
	}
	if p.parameters != nil {
		properties[propertyParameters] = p.parameters
	}
	if p.response != nil {
		properties[propertyResponse] = p.response
	}
	if p.call.IsError {
		properties[propertyErrorType] = p.errorType
		properties[propertyErrorMessage] = p.errorMessage
	}
	return properties
}

func setStringProperty(properties posthog.Properties, key, value string) {
	if value != "" {
		properties[key] = value
	}
}

func applyIdentityProperties(properties posthog.Properties, p preparedToolCall, includeSet bool) {
	removeIdentityControlProperties(properties)
	if len(p.groups) > 0 {
		properties[propertyGroups] = p.groups
	}
	if p.explicitID {
		if includeSet && len(p.setProperties) > 0 {
			properties[propertySet] = p.setProperties
		}
	} else {
		properties[propertyProcessProfile] = false
	}
}

func mergeProperties(base, custom posthog.Properties) posthog.Properties {
	result := make(posthog.Properties, len(base)+len(custom))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range custom {
		result[key] = value
	}
	return result
}

func messageSize(message posthog.Message) (int, error) {
	data, err := marshalJSONSafely(message.APIfy())
	if err != nil {
		return 0, packageError("measure event", err)
	}
	return len(data), nil
}
