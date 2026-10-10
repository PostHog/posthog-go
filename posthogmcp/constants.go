package posthogmcp

const (
	eventToolCall          = "$mcp_tool_call"
	eventUnknownTool       = "$mcp_unknown_tool"
	eventInputRequired     = "$mcp_input_required"
	eventMissingCapability = "$mcp_missing_capability"

	propertySource              = "$mcp_source"
	propertyResourceName        = "$mcp_resource_name"
	propertyToolName            = "$mcp_tool_name"
	propertyToolDescription     = "$mcp_tool_description"
	propertyToolCategory        = "$mcp_tool_category"
	propertyDurationMS          = "$mcp_duration_ms"
	propertyIsError             = "$mcp_is_error"
	propertyParameters          = "$mcp_parameters"
	propertyResponse            = "$mcp_response"
	propertyErrorType           = "$mcp_error_type"
	propertyErrorMessage        = "$mcp_error_message"
	propertyIntent              = "$mcp_intent"
	propertyIntentSource        = "$mcp_intent_source"
	propertyConversationID      = "$mcp_conversation_id"
	propertyLLMModel            = "$mcp_llm_model"
	propertyLLMModelSource      = "$mcp_llm_model_source"
	propertyClientUserAgent     = "$mcp_client_user_agent"
	propertyVendorClient        = "$mcp_vendor_client"
	propertyServerName          = "$mcp_server_name"
	propertyServerVersion       = "$mcp_server_version"
	propertyClientName          = "$mcp_client_name"
	propertyClientVersion       = "$mcp_client_version"
	propertyProtocolVersion     = "$mcp_protocol_version"
	propertyInputRequestMethods = "$mcp_input_request_methods"
	propertySessionID           = "$session_id"
	propertyExceptionLevel      = "$exception_level"
	propertyGroups              = "$groups"
	propertySet                 = "$set"
	propertyProcessProfile      = "$process_person_profile"
	optionProcessProfile        = "process_person_profile"

	analyticsSource = "posthog_mcp_analytics"
)

const (
	maxDepth              = 10
	maxBreadth            = 100
	maxStringBytes        = 32_768
	maxEventBytes         = 102_400
	maxNormalizeBytes     = 1_048_576
	maxRedactBytes        = 33_554_432
	maxRedactBlocks       = 1_000
	maxIntentBytes        = 2_048
	maxErrorMessageBytes  = 2_048
	maxResourceNameBytes  = 256
	maxMetadataBytes      = 256
	maxReturnedErrorBytes = 512
	largeBinaryGateBytes  = 10_240
)

const (
	redactedValue         = "[redacted]"
	binaryRedactedValue   = "[binary data redacted - not supported by PostHog MCP analytics]"
	truncationSuffix      = "..."
	oversizedPayloadValue = "[payload omitted - exceeds MCP analytics input limit]"
)
