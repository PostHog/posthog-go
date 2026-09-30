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

// maxListingPages bounds the tools/list pages one learning walk requests.
const maxListingPages = 100

// toolCatalog remembers every tool seen in a tools/list result. go-sdk has no
// public tool registry, so listings are the only source of tool metadata.
type toolCatalog struct {
	injectContext bool

	mu      sync.Mutex
	current *catalogGeneration
}

// catalogGeneration is what the catalog knows between two invalidations.
type catalogGeneration struct {
	tools map[string]toolInfo
	// walked is nil until a learning walk starts, and closed when it ends.
	// After that a name the walk did not find stays unknown until a tools/list
	// result or an invalidation.
	walked chan struct{}
}

func newToolCatalog(injectContext bool) *toolCatalog {
	return &toolCatalog{injectContext: injectContext, current: newCatalogGeneration()}
}

func newCatalogGeneration() *catalogGeneration {
	return &catalogGeneration{tools: map[string]toolInfo{}}
}

// invalidate starts a new generation that knows no tools.
func (c *toolCatalog) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = newCatalogGeneration()
}

func (c *toolCatalog) generation() *catalogGeneration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// advertise records a tools/list result and returns its tools as clients
// should see them. The registered tools are never modified.
func (c *toolCatalog) advertise(gen *catalogGeneration, tools []*mcpsdk.Tool) []*mcpsdk.Tool {
	advertised := c.record(gen, tools)
	c.mu.Lock()
	defer c.mu.Unlock()
	gen.walked = nil
	return advertised
}

// record remembers tools in gen unless gen has been invalidated, and returns
// them as clients should see them.
func (c *toolCatalog) record(gen *catalogGeneration, tools []*mcpsdk.Tool) []*mcpsdk.Tool {
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
	if gen == c.current {
		for i, tool := range tools {
			gen.tools[tool.Name] = infos[i]
		}
	}
	return advertised
}

// lookup returns what the catalog knows about the called tool. A call that
// reaches this process before any tools/list did, as when a load balancer
// sends a client's listing and its calls to different replicas, lists tools
// through next once per generation. Concurrent callers share that walk.
func (c *toolCatalog) lookup(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) toolInfo {
	name := req.Params.Name
	c.mu.Lock()
	gen := c.current
	info, known := gen.tools[name]
	walked := gen.walked
	leads := !known && walked == nil && req.Session != nil
	if leads {
		walked = make(chan struct{})
		gen.walked = walked
	}
	c.mu.Unlock()
	if known || walked == nil {
		return info
	}

	if leads {
		defer close(walked)
		c.walk(ctx, next, req, gen)
	} else {
		select {
		case <-walked:
		case <-ctx.Done():
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return gen.tools[name]
}

func (c *toolCatalog) walk(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest, gen *catalogGeneration) {
	list := &mcpsdk.ListToolsRequest{Session: req.Session, Params: &mcpsdk.ListToolsParams{}, Extra: req.Extra}
	for range maxListingPages {
		result, err := next(ctx, methodListTools, list)
		page, ok := result.(*mcpsdk.ListToolsResult)
		if err != nil || !ok {
			return
		}
		c.record(gen, page.Tools)
		if page.NextCursor == "" {
			return
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
