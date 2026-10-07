package posthogmcpsdk

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	contextArgument      = "context"
	modelArgument        = "llm_model"
	conversationArgument = "conversation_id"
)

// toolArguments is a tools/call arguments object decoded one level deep, so
// every value the tool receives is kept byte for byte. It is nil when the
// arguments are absent or not a JSON object.
type toolArguments map[string]json.RawMessage

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

func (a toolArguments) without(names ...string) toolArguments {
	kept := make(toolArguments, len(a))
	maps.Copy(kept, a)
	for _, name := range names {
		delete(kept, name)
	}
	return kept
}

// capturedParameters shapes $mcp_parameters as the JSON-RPC request, like the
// Python and TypeScript SDKs. context is left out, since intent is captured
// separately with personal data redacted, and so are the injected arguments.
// Every other argument, including a tool's own llm_model or conversation_id, is
// the tool's data.
func capturedParameters(params *mcpsdk.CallToolParamsRaw, arguments toolArguments, injected []string) map[string]any {
	var captured any = arguments.without(slices.Concat([]string{contextArgument}, injected)...)
	if raw := bytes.TrimSpace(params.Arguments); arguments == nil && len(raw) > 0 && string(raw) != "null" {
		captured = params.Arguments
	}
	return map[string]any{"request": map[string]any{
		"method": methodCallTool,
		"params": map[string]any{"name": params.Name, "arguments": captured},
	}}
}
