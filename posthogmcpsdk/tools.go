package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const contextParameterDescription = "Explain in 15-25 words, in third person, why this tool is called and how it supports " +
	"the user's goal. For analytics only. You MUST describe only the abstract purpose of the " +
	"tool call. NEVER include, repeat, paraphrase, or infer personal, sensitive, or identifying " +
	"information from the user request or tool results, including names, emails, phone numbers, " +
	`IPs, IDs, or credentials. You MUST generalize specific entities into roles such as "a user", ` +
	`"the customer", or "an account". Example: "Retrieving a customer's recent orders to ` +
	`investigate a billing issue and help support determine the appropriate resolution."`

// toolInfo is what instrumentation knows about a registered tool from its
// tools/list entry.
type toolInfo struct {
	description string
	category    string
	// contextInjected means the advertised schema has a context argument the
	// tool itself does not declare, so it is removed before dispatch.
	contextInjected bool
}

// toolCatalog remembers every tool seen in a tools/list result. go-sdk has no
// public tool registry, so listings are the only source of tool metadata.
type toolCatalog struct {
	injectContext bool

	mu    sync.Mutex
	tools map[string]toolInfo
}

func newToolCatalog(injectContext bool) *toolCatalog {
	return &toolCatalog{injectContext: injectContext, tools: map[string]toolInfo{}}
}

func (c *toolCatalog) get(name string) (toolInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, ok := c.tools[name]
	return info, ok
}

// advertise records tools and returns them as clients should see them. The
// registered tools are never modified.
func (c *toolCatalog) advertise(tools []*mcpsdk.Tool) []*mcpsdk.Tool {
	advertised := make([]*mcpsdk.Tool, len(tools))
	infos := make([]toolInfo, len(tools))
	for i, tool := range tools {
		advertised[i] = tool
		infos[i] = toolInfo{description: tool.Description}
		infos[i].category, _ = tool.Meta["category"].(string)
		if !c.injectContext {
			continue
		}
		if schema, ok := withContextParameter(tool.InputSchema); ok {
			copied := *tool
			copied.InputSchema = schema
			advertised[i] = &copied
			infos[i].contextInjected = true
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, tool := range tools {
		c.tools[tool.Name] = infos[i]
	}
	return advertised
}

// learn lists tools through next until the called tool appears, for a call that
// reaches this process before any tools/list did, as when a load balancer
// sends a client's listing and its calls to different replicas.
func (c *toolCatalog) learn(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) toolInfo {
	if req.Session == nil {
		return toolInfo{}
	}
	list := &mcpsdk.ListToolsRequest{Session: req.Session, Params: &mcpsdk.ListToolsParams{}, Extra: req.Extra}
	for {
		result, err := next(ctx, methodListTools, list)
		page, ok := result.(*mcpsdk.ListToolsResult)
		if err != nil || !ok {
			return toolInfo{}
		}
		c.advertise(page.Tools)
		if info, ok := c.get(req.Params.Name); ok {
			return info
		}
		if page.NextCursor == "" || page.NextCursor == list.Params.Cursor {
			return toolInfo{}
		}
		list.Params = &mcpsdk.ListToolsParams{Cursor: page.NextCursor}
	}
}

// withContextParameter returns a copy of inputSchema that declares a required
// context string. Schemas whose properties cannot be extended safely, or that
// already declare context, are left alone. The advertised schema may require
// context even under additionalProperties false: go-sdk validates calls
// against the registered schema, and the argument is removed first.
func withContextParameter(inputSchema any) (map[string]any, bool) {
	encoded, err := json.Marshal(inputSchema)
	if err != nil {
		return nil, false
	}
	var schema map[string]any
	if json.Unmarshal(encoded, &schema) != nil || schema == nil {
		return nil, false
	}
	for _, key := range []string{"$ref", "allOf", "anyOf", "oneOf"} {
		if _, ok := schema[key]; ok {
			return nil, false
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		if schema["properties"] != nil {
			return nil, false
		}
		properties = map[string]any{}
	}
	if _, declared := properties[contextArgument]; declared {
		return nil, false
	}

	properties[contextArgument] = map[string]any{"type": "string", "description": contextParameterDescription}
	schema["properties"] = properties
	required, _ := schema["required"].([]any)
	schema["required"] = append(required, contextArgument)
	return schema, true
}
