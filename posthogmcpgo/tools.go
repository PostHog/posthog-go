package posthogmcpgo

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

const contextParameterDescription = "Explain in 15-25 words, in third person, why this tool is called and how it supports " +
	"the user's goal. For analytics only. You MUST describe only the abstract purpose of the " +
	"tool call. NEVER include, repeat, paraphrase, or infer personal, sensitive, or identifying " +
	"information from the user request or tool results, including names, emails, phone numbers, " +
	`IPs, IDs, or credentials. You MUST generalize specific entities into roles such as "a user", ` +
	`"the customer", or "an account". Example: "Retrieving a customer's recent orders to ` +
	`investigate a billing issue and help support determine the appropriate resolution."`

const modelParameterDescription = "The exact model identifier you (the assistant) are running as, taken from your " +
	`system prompt or environment (e.g. "claude-opus-4-8", "gpt-5.2"). Used for analytics only. If you do not ` +
	`know your model identifier with certainty, pass "unknown" — never guess.`

// analyticsArgument is an argument the instrumentation advertises on every
// tool that does not declare it, and removes from calls before the handler.
type analyticsArgument struct {
	name        string
	description string
}

var (
	contextParameter = analyticsArgument{name: contextArgument, description: contextParameterDescription}
	modelParameter   = analyticsArgument{name: modelArgument, description: modelParameterDescription}
)

// toolInfo is what instrumentation knows about a registered tool.
type toolInfo struct {
	description string
	category    string
	// injected names the analytics arguments the advertised schema adds to the
	// tool's own, which are removed before the handler sees the call.
	injected []string
}

// present returns tool as clients should see it, with the analytics arguments
// it does not declare added to its input schema, and what instrumentation
// knows about it. The registered tool is never modified.
func present(tool mcp.Tool, arguments []analyticsArgument) (mcp.Tool, toolInfo) {
	info := toolInfo{description: tool.Description}
	if tool.Meta != nil {
		info.category, _ = tool.Meta.AdditionalFields["category"].(string)
	}
	schema, injected := withArguments(inputSchema(tool), arguments)
	if len(injected) == 0 {
		return tool, info
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return tool, info
	}
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = encoded
	info.injected = injected
	return tool, info
}

func inputSchema(tool mcp.Tool) json.RawMessage {
	if tool.RawInputSchema != nil {
		return tool.RawInputSchema
	}
	encoded, _ := json.Marshal(tool.InputSchema)
	return encoded
}

// withArguments returns a copy of inputSchema that declares each of arguments
// the schema does not, all required, and the names it added. A schema whose
// properties cannot be extended safely is left alone.
func withArguments(inputSchema json.RawMessage, arguments []analyticsArgument) (map[string]any, []string) {
	schema, properties, ok := extensibleSchema(inputSchema)
	if !ok {
		return nil, nil
	}
	required, _ := schema["required"].([]any)
	var injected []string
	for _, argument := range arguments {
		if _, declared := properties[argument.name]; declared {
			continue
		}
		properties[argument.name] = map[string]any{"type": "string", "description": argument.description}
		required = append(required, argument.name)
		injected = append(injected, argument.name)
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, injected
}

// extensibleSchema decodes a JSON schema whose properties can be extended
// safely, and its properties: an object schema, or one without a type, that
// allows other properties, with no $ref, allOf, anyOf, or oneOf at its root,
// whose properties, if any, are an object.
func extensibleSchema(raw json.RawMessage) (schema, properties map[string]any, ok bool) {
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return nil, nil, false
	}
	if kind := schema["type"]; kind != nil && kind != "object" {
		return nil, nil, false
	}
	// mcp-go's WithInputSchemaValidation checks a call against the registered
	// schema before any middleware runs, so it would reject the added arguments.
	if schema["additionalProperties"] == false {
		return nil, nil, false
	}
	for _, key := range []string{"$ref", "allOf", "anyOf", "oneOf"} {
		if _, blocked := schema[key]; blocked {
			return nil, nil, false
		}
	}
	properties, ok = schema["properties"].(map[string]any)
	if !ok {
		if schema["properties"] != nil {
			return nil, nil, false
		}
		properties = map[string]any{}
		schema["properties"] = properties
	}
	return schema, properties, true
}
