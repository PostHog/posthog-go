package posthogmcp

import (
	"errors"
	"fmt"
	"strings"
	"time"

	posthog "github.com/posthog/posthog-go/v2"
)

type preparedToolCall struct {
	call                  ToolCall
	distinctID            string
	explicitID            bool
	sessionID             string
	conversationID        string
	clientUserAgent       string
	vendorClient          string
	toolName              string
	intent                string
	intentSource          IntentSource
	model                 string
	modelSource           ModelSource
	errorType             string
	exceptionType         string
	errorMessage          string
	suppressPersonProfile bool
	parameters            any
	response              any
	groups                posthog.Groups
	setProperties         posthog.Properties
	custom                posthog.Properties
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

	if call.Error != nil && exceptionAutocapture {
		exception, err := prepared.buildException()
		if err != nil {
			return nil, err
		}
		messages = append(messages, namedMessage{name: "$exception", message: exception})
	}
	return messages, nil
}

// emptyJSONObject is what a client sends for an empty context, which is no intent.
const emptyJSONObject = "{}"

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

	call.IsError = call.IsError || call.Error != nil
	explicitID := call.DistinctID != ""
	prepared := preparedToolCall{
		call:                  call,
		explicitID:            explicitID,
		suppressPersonProfile: !explicitID || personProfileOptOut(call.Properties),
		toolName:              truncateUTF8(sanitizeResourceName(call.ToolName), maxResourceNameBytes),
	}
	prepared.conversationID = normalizeConversationID(call.ConversationID)
	prepared.sessionID = call.SessionID
	if prepared.conversationID != "" {
		prepared.sessionID = deriveSessionID(prepared.conversationID)
	}
	if prepared.sessionID == "" {
		prepared.sessionID = mintSessionID()
	}
	switch {
	case call.DistinctID != "":
		prepared.distinctID = call.DistinctID
	case prepared.sessionID != "":
		prepared.distinctID = prepared.sessionID
	default:
		prepared.distinctID = "anonymous"
	}

	intent := strings.TrimSpace(call.Intent)
	if intent != "" && intent != emptyJSONObject {
		prepared.intent = truncateUTF8(sanitizeFreeText(truncateUTF8(intent, 2*maxIntentBytes)), maxIntentBytes)
		prepared.intentSource = call.IntentSource
		if prepared.intentSource == "" {
			prepared.intentSource = IntentSourceContextParameter
		}
	}

	prepared.clientUserAgent = boundedClientHeader(call.ClientUserAgent)
	prepared.vendorClient = boundedClientHeader(call.VendorClient)
	prepared.model = normalizeModel(call.LLMModel)
	if prepared.model != "" {
		prepared.modelSource = call.LLMModelSource
		if prepared.modelSource != "" &&
			prepared.modelSource != ModelSourceClientMetadata &&
			prepared.modelSource != ModelSourceSelfReported {
			return preparedToolCall{}, errors.New("posthogmcp: invalid LLMModelSource")
		}
		if prepared.modelSource == "" {
			prepared.modelSource = ModelSourceSelfReported
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
	custom := make(posthog.Properties, len(call.Properties))
	for key, value := range call.Properties {
		if strings.HasPrefix(key, "$mcp_") {
			continue
		}
		switch key {
		case propertyGroups, propertySet, propertyProcessProfile, propertySessionID, propertyExceptionLevel:
			continue
		}
		custom[key] = value
	}
	prepared.custom, err = prepareProperties("Properties", custom)
	if err != nil {
		return preparedToolCall{}, err
	}

	if call.IsError {
		prepared.exceptionType = errorTypeName(call.Error)
		if prepared.exceptionType == "" {
			prepared.exceptionType = defaultErrorType
		}
		prepared.exceptionType = truncateUTF8(prepared.exceptionType, maxMetadataBytes)
		prepared.errorType = truncateUTF8(strings.TrimSpace(call.ErrorType), maxMetadataBytes)
		if prepared.errorType == "" {
			prepared.errorType = prepared.exceptionType
		}

		prepared.errorMessage = fmt.Sprintf("Tool %s returned an error", prepared.toolName)
		if call.Error != nil {
			provided, err := safeErrorMessage(call.Error)
			if err != nil {
				return preparedToolCall{}, err
			}
			if strings.TrimSpace(provided) != "" {
				prepared.errorMessage = truncateUTF8(sanitizeString(provided), maxErrorMessageBytes)
			}
		}
	}

	return prepared, nil
}

func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "unknown") {
		return ""
	}
	return boundedMetadata(model)
}

func boundedMetadata(value string) string {
	return truncateUTF8(sanitizeString(value), maxMetadataBytes)
}

// boundedClientHeader redacts known credential shapes without the entropy
// detector, which reads version tokens such as AppleWebKit/537.36 as secrets
// and would erase the headers that identify the calling client.
func boundedClientHeader(value string) string {
	return truncateUTF8(sanitizeResourceName(value), maxMetadataBytes)
}

func prepareValue(field string, value any, response bool) (any, error) {
	if value == nil {
		return nil, nil
	}
	if response {
		value = redactMediaBeforeNormalize(value)
	}
	normalized, err := normalizePayload(field, value)
	if err != nil {
		return nil, err
	}
	if response {
		return truncateValue(sanitizeResponse(normalized)), nil
	}
	return truncateValue(sanitizeCapturedValue(normalized)), nil
}

func prepareProperties(field string, properties posthog.Properties) (posthog.Properties, error) {
	if len(properties) == 0 {
		return nil, nil
	}
	normalized, err := normalizePayload(field, properties)
	if err != nil {
		return nil, err
	}
	value, ok := truncateValue(sanitizeMetadataValue(normalized)).(map[string]any)
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
	value, ok := truncateValue(sanitizeMetadataValue(normalized)).(map[string]any)
	if !ok {
		return nil, errors.New("posthogmcp: normalized groups must be an object")
	}
	prepared := make(posthog.Groups, len(value))
	for groupType, groupKey := range value {
		key, ok := groupKey.(string)
		if !ok {
			return nil, errors.New("posthogmcp: normalized group keys must be strings")
		}
		prepared[groupType] = key
	}
	return prepared, nil
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

func personProfileOptOut(properties posthog.Properties) bool {
	value, ok := properties[propertyProcessProfile]
	if !ok {
		return false
	}
	disabled, ok := value.(bool)
	return ok && !disabled
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
	p.setIdentityProperties(properties)
	p.setModelAndIntentProperties(properties)
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

func (p preparedToolCall) setModelAndIntentProperties(properties posthog.Properties) {
	setStringProperty(properties, propertyLLMModel, p.model)
	if p.model != "" {
		properties[propertyLLMModelSource] = string(p.modelSource)
	}
	setStringProperty(properties, propertyIntent, p.intent)
	if p.intent != "" {
		properties[propertyIntentSource] = string(p.intentSource)
	}
}

// setIdentityProperties sets the session, conversation, client and server
// identity every MCP event carries.
func (p preparedToolCall) setIdentityProperties(properties posthog.Properties) {
	setStringProperty(properties, propertySessionID, p.sessionID)
	setStringProperty(properties, propertyConversationID, p.conversationID)
	setStringProperty(properties, propertyClientUserAgent, p.clientUserAgent)
	setStringProperty(properties, propertyVendorClient, p.vendorClient)
	setStringProperty(properties, propertyServerName, truncateUTF8(p.call.ServerName, maxMetadataBytes))
	setStringProperty(properties, propertyServerVersion, truncateUTF8(p.call.ServerVersion, maxMetadataBytes))
	setStringProperty(properties, propertyClientName, truncateUTF8(p.call.ClientName, maxMetadataBytes))
	setStringProperty(properties, propertyClientVersion, truncateUTF8(p.call.ClientVersion, maxMetadataBytes))
	setStringProperty(properties, propertyProtocolVersion, truncateUTF8(p.call.ProtocolVersion, maxMetadataBytes))
}

func setStringProperty(properties posthog.Properties, key, value string) {
	if value != "" {
		properties[key] = value
	}
}

func applyIdentityProperties(properties posthog.Properties, p preparedToolCall, includeSet bool) {
	if len(p.groups) > 0 {
		properties[propertyGroups] = p.groups
	}
	if p.explicitID {
		if includeSet && len(p.setProperties) > 0 {
			properties[propertySet] = p.setProperties
		}
	}
	if p.suppressPersonProfile {
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
