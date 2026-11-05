package posthogmcpgo

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	methodCallTool  = "tools/call"
	contextArgument = "context"
	modelArgument   = "llm_model"

	// emptyJSONObject is what a client sends for an empty context, which is no
	// intent, as in posthogmcp.
	emptyJSONObject = "{}"
)

// toolArguments is a tools/call arguments object decoded one level deep, so
// every value the tool receives is kept byte for byte. It is nil when the
// arguments are absent or not a JSON object.
type toolArguments map[string]json.RawMessage

// rawArguments is the arguments JSON as the client sent it.
func rawArguments(params mcp.CallToolParams) json.RawMessage {
	if len(params.RawArguments) > 0 {
		return params.RawArguments
	}
	if params.Arguments == nil {
		return nil
	}
	encoded, _ := json.Marshal(params.Arguments)
	return encoded
}

func parseArguments(raw json.RawMessage) toolArguments {
	var arguments toolArguments
	if json.Unmarshal(raw, &arguments) != nil {
		return nil
	}
	return arguments
}

func (a toolArguments) text(name string) string {
	var value string
	if json.Unmarshal(a[name], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (a toolArguments) hasAny(names []string) bool {
	return slices.ContainsFunc(names, func(name string) bool {
		_, ok := a[name]
		return ok
	})
}

// without returns a copy of a without names, empty rather than nil when a is
// absent, as posthogmcpsdk captures absent arguments.
func (a toolArguments) without(names ...string) toolArguments {
	kept := make(toolArguments, len(a))
	maps.Copy(kept, a)
	for _, name := range names {
		delete(kept, name)
	}
	return kept
}

// withoutArguments returns req with names removed from both of mcp-go's
// copies of the arguments, so neither GetArguments nor BindArguments sees them.
func withoutArguments(req mcp.CallToolRequest, arguments toolArguments, names []string) (mcp.CallToolRequest, error) {
	raw, err := json.Marshal(arguments.without(names...))
	if err != nil {
		return req, err
	}
	decoded, ok := req.Params.Arguments.(map[string]any)
	if ok {
		decoded = maps.Clone(decoded)
		for _, name := range names {
			delete(decoded, name)
		}
	} else if err := json.Unmarshal(raw, &decoded); err != nil {
		return req, err
	}
	req.Params.Arguments = decoded
	req.Params.RawArguments = raw
	return req, nil
}

// capturedParameters shapes $mcp_parameters as the JSON-RPC request, like the
// other SDKs. context is left out, since intent is captured separately with
// personal data redacted, and so are the injected arguments. Every other
// argument, including a tool's own llm_model, is the tool's data.
func capturedParameters(name string, raw json.RawMessage, arguments toolArguments, injected []string) map[string]any {
	var captured any = arguments.without(slices.Concat([]string{contextArgument}, injected)...)
	if trimmed := bytes.TrimSpace(raw); arguments == nil && len(trimmed) > 0 && string(trimmed) != "null" {
		captured = raw
	}
	return map[string]any{"request": map[string]any{
		"method": methodCallTool,
		"params": map[string]any{"name": name, "arguments": captured},
	}}
}
