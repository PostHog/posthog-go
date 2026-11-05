package posthogmcpgo

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

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

func (argument analyticsArgument) schema() map[string]any {
	return map[string]any{"type": "string", "description": argument.description}
}

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
// knows about it. The schema keeps its representation, typed or raw, and the
// registered tool is never modified.
func present(tool mcp.Tool, arguments []analyticsArgument) (mcp.Tool, toolInfo) {
	info := toolInfo{description: tool.Description}
	if tool.Meta != nil {
		info.category, _ = tool.Meta.AdditionalFields["category"].(string)
	}
	schema, ok := extensibleSchema(inputSchema(tool))
	if !ok {
		return tool, info
	}
	properties, _ := schema["properties"].(map[string]any)
	var added []analyticsArgument
	for _, argument := range arguments {
		if _, declared := properties[argument.name]; !declared {
			added = append(added, argument)
		}
	}
	advertised, err := withArguments(tool, schema, added)
	if len(added) == 0 || err != nil {
		return tool, info
	}
	for _, argument := range added {
		info.injected = append(info.injected, argument.name)
	}
	return advertised, info
}

func inputSchema(tool mcp.Tool) json.RawMessage {
	if tool.RawInputSchema != nil {
		return tool.RawInputSchema
	}
	encoded, _ := json.Marshal(tool.InputSchema)
	return encoded
}

// withArguments returns tool with arguments added to its input schema, all
// required, in the representation the tool uses. schema is the decoded raw
// schema, which it may change.
func withArguments(tool mcp.Tool, schema map[string]any, arguments []analyticsArgument) (mcp.Tool, error) {
	if tool.RawInputSchema != nil {
		properties, _ := schema["properties"].(map[string]any)
		if properties == nil {
			properties = map[string]any{}
			schema["properties"] = properties
		}
		required, _ := schema["required"].([]any)
		for _, argument := range arguments {
			properties[argument.name] = argument.schema()
			required = append(required, argument.name)
		}
		schema["required"] = required
		encoded, err := json.Marshal(schema)
		tool.RawInputSchema = encoded
		return tool, err
	}
	properties := maps.Clone(tool.InputSchema.Properties)
	if properties == nil {
		properties = map[string]any{}
	}
	required := slices.Clone(tool.InputSchema.Required)
	for _, argument := range arguments {
		properties[argument.name] = argument.schema()
		required = append(required, argument.name)
	}
	tool.InputSchema.Properties, tool.InputSchema.Required = properties, required
	return tool, nil
}

// openSchemaKeywords are the root keywords that cannot make a schema reject an
// extra property. mcp-go's WithInputSchemaValidation checks a call against the
// registered schema before any middleware runs, so a schema with any other
// root keyword (unevaluatedProperties, maxProperties, propertyNames,
// patternProperties, not, if, $ref, allOf, and the rest) could reject the
// added arguments, and is left unchanged. minProperties is among them: the
// added arguments would satisfy it, and removing them would not.
var openSchemaKeywords = map[string]func(value any) bool{
	"type":                 func(value any) bool { return value == "object" },
	"properties":           isObject,
	"required":             isArray,
	"additionalProperties": func(value any) bool { return value == true },
	"title":                anything,
	"description":          anything,
	"$schema":              anything,
	"$id":                  anything,
	"$comment":             anything,
	"$defs":                anything,
	"definitions":          anything,
	"examples":             anything,
	"default":              anything,
	"deprecated":           anything,
	"readOnly":             anything,
	"writeOnly":            anything,
}

// refersToRoot is whether a "$ref": "#" appears anywhere in a decoded schema.
func refersToRoot(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if value["$ref"] == "#" {
			return true
		}
		for _, nested := range value {
			if refersToRoot(nested) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(value, refersToRoot)
	}
	return false
}

func isObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func isArray(value any) bool {
	_, ok := value.([]any)
	return ok
}

func anything(any) bool { return true }

// extensibleSchema decodes a JSON schema that accepts any extra property, so
// arguments can be added to it. Numbers stay json.Number, so a large integer
// survives being encoded again. A schema that refers back to its root is left
// out, since a required argument added at the root would also apply to every
// nested object that refers to it.
func extensibleSchema(raw json.RawMessage) (map[string]any, bool) {
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil || decoder.More() || schema == nil || refersToRoot(schema) {
		return nil, false
	}
	for keyword, value := range schema {
		if allowed, known := openSchemaKeywords[keyword]; !known || !allowed(value) {
			return nil, false
		}
	}
	return schema, true
}
