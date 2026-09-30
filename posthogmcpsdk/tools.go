package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

// missTTL is how long a completed walk vouches that a name it did not find is
// not registered. go-sdk sends no list_changed without a connected session,
// so on a stateless server this is what lets a tool added at runtime become
// known. Ten seconds keeps steady calls for unregistered names to one walk,
// which runs in process, per ten seconds, and has a tool added at runtime
// recognized within ten seconds.
const missTTL = 10 * time.Second

// toolCatalog remembers every tool seen in a tools/list result. go-sdk has no
// public tool registry, so listings are the only source of tool metadata.
type toolCatalog struct {
	injectContext bool
	now           func() time.Time

	mu      sync.Mutex
	current *catalogGeneration
}

// catalogGeneration is what the catalog knows between two invalidations.
type catalogGeneration struct {
	tools map[string]toolInfo
	// walking is non-nil while a learning walk runs, and closed when it ends.
	walking chan struct{}
	// walkedAt is when the last complete walk ended, zero if a tools/list
	// result came after it. Until missTTL later, a name the catalog does not
	// know is not registered.
	walkedAt time.Time
}

func newToolCatalog(injectContext bool) *toolCatalog {
	return &toolCatalog{injectContext: injectContext, now: time.Now, current: newCatalogGeneration()}
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

// advertise remembers tools in gen unless gen has been invalidated, and
// returns them as clients should see them. The registered tools are never
// modified.
func (c *toolCatalog) advertise(gen *catalogGeneration, tools []*mcpsdk.Tool) []*mcpsdk.Tool {
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
		gen.walkedAt = time.Time{}
	}
	return advertised
}

// lookup returns what the catalog knows about the called tool. A call that
// reaches this process before any tools/list did, as when a load balancer
// sends a client's listing and its calls to different replicas, lists tools
// through next. Concurrent callers share that walk, and once one completes
// the generation remembers for missTTL which names are not registered. The
// error reports why a walk did not complete, which never reaches the
// tools/call.
func (c *toolCatalog) lookup(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) (toolInfo, error) {
	name := req.Params.Name
	c.mu.Lock()
	gen := c.current
	info, known := gen.tools[name]
	missRemembered := !gen.walkedAt.IsZero() && c.now().Sub(gen.walkedAt) < missTTL
	if known || missRemembered || req.Session == nil {
		c.mu.Unlock()
		return info, nil
	}
	walking := gen.walking
	leads := walking == nil
	if leads {
		walking = make(chan struct{})
		gen.walking = walking
	}
	c.mu.Unlock()

	var err error
	if leads {
		err = c.walk(ctx, next, req, gen, walking)
	} else {
		select {
		case <-walking:
		case <-ctx.Done():
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return gen.tools[name], err
}

// walk lists every tool into gen and then ends the walk, waking its waiters.
// Only a walk that reaches the last page lets gen remember misses; otherwise
// the next lookup walks again.
func (c *toolCatalog) walk(
	ctx context.Context,
	next mcpsdk.MethodHandler,
	req *mcpsdk.CallToolRequest,
	gen *catalogGeneration,
	walking chan struct{},
) error {
	complete := false
	defer func() {
		c.mu.Lock()
		gen.walking = nil
		if complete {
			gen.walkedAt = c.now()
		}
		c.mu.Unlock()
		close(walking)
	}()
	list := &mcpsdk.ListToolsRequest{Session: req.Session, Params: &mcpsdk.ListToolsParams{}, Extra: req.Extra}
	for range maxListingPages {
		result, err := listThrough(ctx, next, list)
		if err != nil {
			return err
		}
		page, ok := result.(*mcpsdk.ListToolsResult)
		if !ok {
			return fmt.Errorf("posthogmcpsdk: tools/list through inner handler returned %T", result)
		}
		c.advertise(gen, page.Tools)
		if page.NextCursor == "" {
			complete = true
			return nil
		}
		list.Params = &mcpsdk.ListToolsParams{Cursor: page.NextCursor}
	}
	return fmt.Errorf("posthogmcpsdk: tools/list through inner handler has more than %d pages", maxListingPages)
}

func listThrough(ctx context.Context, next mcpsdk.MethodHandler, list *mcpsdk.ListToolsRequest) (result mcpsdk.Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("posthogmcpsdk: tools/list through inner handler panicked (%T)", recovered)
		}
	}()
	result, err = next(ctx, methodListTools, list)
	if err != nil {
		return nil, fmt.Errorf("posthogmcpsdk: tools/list through inner handler: %w", err)
	}
	return result, nil
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
