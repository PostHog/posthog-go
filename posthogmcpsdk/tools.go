package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

const modelParameterDescription = "The exact model identifier you (the assistant) are running as, taken from your " +
	`system prompt or environment (e.g. "claude-opus-4-8", "gpt-5.2"). Used for analytics only. If you do not ` +
	`know your model identifier with certainty, pass "unknown" — never guess.`

const conversationParameterDescription = "Pass the exact conversation_id from the server's previous response, unchanged. " +
	"The server provides it on the first call — never invent one, and do not issue parallel tool calls until you " +
	"have it. Keep passing the same conversation_id for the rest of the conversation, including after later user " +
	"messages or on a different task; do not reset it when the user starts a new request."

const defaultMissingCapabilityTool = "get_more_tools"

const missingCapabilityDescription = "Check for additional tools whenever your task might benefit from specialized " +
	"capabilities - even if existing tools could work as a fallback."

const missingCapabilityContextDescription = "A description of your goal and what kind of tool would help accomplish it. " +
	"Leave out personal and identifying information such as names, emails, and IDs; describe people by role, " +
	`like "a customer".`

const missingCapabilityResultText = "Unfortunately, we have shown you the full tool list. We have noted your feedback " +
	"and will work to improve the tool list in the future."

func missingCapabilityTool(name string) *mcpsdk.Tool {
	openWorld := true
	destructive := false
	return &mcpsdk.Tool{
		Name:        name,
		Description: missingCapabilityDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				contextArgument: map[string]any{"type": "string", "description": missingCapabilityContextDescription},
			},
			"required": []any{contextArgument},
		},
		Annotations: &mcpsdk.ToolAnnotations{
			Title:           "Get More Tools",
			ReadOnlyHint:    true,
			OpenWorldHint:   &openWorld,
			IdempotentHint:  true,
			DestructiveHint: &destructive,
		},
	}
}

func missingCapabilityResult() *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: missingCapabilityResultText}}}
}

// analyticsArgument is an argument the middleware advertises on every tool
// that does not declare it, and removes from calls before dispatch.
type analyticsArgument struct {
	name        string
	description string
	required    bool
}

var (
	contextParameter = analyticsArgument{name: contextArgument, description: contextParameterDescription, required: true}
	modelParameter   = analyticsArgument{name: modelArgument, description: modelParameterDescription, required: true}
	// conversationParameter is optional because the agent has no handle on its
	// first call.
	conversationParameter = analyticsArgument{name: conversationArgument, description: conversationParameterDescription}
)

// toolInfo is what instrumentation knows about a registered tool from its
// tools/list entry.
type toolInfo struct {
	// registered means the server listed the tool, as opposed to a name the
	// catalog has not learned.
	registered  bool
	description string
	category    string
	// injected names the analytics arguments the advertised schema adds to the
	// tool's own, which are removed before dispatch.
	injected []string
	// instructions means the advertised output schema declares
	// _mcp_instructions, so results carry the conversation handle there too.
	instructions bool
}

func (info toolInfo) injects(argument string) bool {
	return slices.Contains(info.injected, argument)
}

// maxListingPages bounds the tools/list pages one learning walk requests.
const maxListingPages = 100

// catalogTTL is how long what the catalog learned is trusted: a tool's entry
// after it was learned, and the absence of a name after a completed walk.
// go-sdk sends no list_changed without a connected session, so on a stateless
// server this is what lets a tool added or replaced at runtime be learned
// again. Ten seconds keeps steady calls to one walk, which runs in process,
// per ten seconds, and has such a change recognized within ten seconds.
const catalogTTL = 10 * time.Second

// toolCatalog remembers every tool seen in a tools/list result. go-sdk has no
// public tool registry, so listings are the only source of tool metadata.
type toolCatalog struct {
	inject []analyticsArgument
	now    func() time.Time

	mu      sync.Mutex
	current *catalogGeneration
}

// catalogGeneration is what the catalog knows between two invalidations.
type catalogGeneration struct {
	tools map[string]catalogEntry
	// walking is non-nil while a learning walk runs, and closed when it ends.
	walking chan struct{}
	// walkedAt is when the last finished walk ended. Until catalogTTL later, a
	// name the catalog does not know is not listed again (past the page cap it
	// may be registered): a later tools/list result can only add names, which
	// are then known.
	walkedAt time.Time
}

type catalogEntry struct {
	info      toolInfo
	learnedAt time.Time
}

func newToolCatalog(inject []analyticsArgument, now func() time.Time) *toolCatalog {
	return &toolCatalog{inject: inject, now: now, current: newCatalogGeneration()}
}

func newCatalogGeneration() *catalogGeneration {
	return &catalogGeneration{tools: map[string]catalogEntry{}}
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
		advertised[i], infos[i] = c.present(tool)
		infos[i].registered = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.current {
		for i, tool := range tools {
			gen.tools[tool.Name] = catalogEntry{info: infos[i], learnedAt: c.now()}
		}
	}
	return advertised
}

// present returns tool as clients should see it, and what instrumentation
// learns from it.
func (c *toolCatalog) present(tool *mcpsdk.Tool) (*mcpsdk.Tool, toolInfo) {
	info := toolInfo{description: tool.Description}
	info.category, _ = tool.Meta["category"].(string)
	schema, injected := withArguments(tool.InputSchema, c.inject)
	if len(injected) == 0 {
		return tool, info
	}
	copied := *tool
	copied.InputSchema = schema
	info.injected = injected
	if slices.Contains(injected, conversationArgument) {
		copied.OutputSchema, info.instructions = withInstructions(tool.OutputSchema)
	}
	return &copied, info
}

// registers reports whether gen learned a tool of that name.
func (c *toolCatalog) registers(gen *catalogGeneration, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return gen.tools[name].info.registered
}

// lookup returns what the catalog knows about the called tool. A call that
// reaches this process before any tools/list did, as when a load balancer
// sends a client's listing and its calls to different replicas, lists tools
// through next, and so does a call for a tool learned catalogTTL ago, which
// may have been replaced since. Concurrent callers share that walk, and once
// one completes the generation remembers for catalogTTL that it did not know a
// name. The error reports why a walk did not complete, which never
// reaches the tools/call.
func (c *toolCatalog) lookup(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) (toolInfo, error) {
	info, gen, err := c.lookupIn(ctx, next, req)
	if err == nil && gen != c.generation() {
		// The tools changed during the walk, so what it learned went to a
		// generation nobody reads. Learn the current one instead.
		info, _, err = c.lookupIn(ctx, next, req)
	}
	return info, err
}

func (c *toolCatalog) lookupIn(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) (toolInfo, *catalogGeneration, error) {
	name := req.Params.Name
	c.mu.Lock()
	gen := c.current
	entry, known := gen.tools[name]
	now := c.now()
	trusted := known && now.Sub(entry.learnedAt) < catalogTTL
	missRemembered := !gen.walkedAt.IsZero() && now.Sub(gen.walkedAt) < catalogTTL
	if trusted || missRemembered || req.Session == nil {
		c.mu.Unlock()
		return entry.info, gen, nil
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
	return gen.tools[name].info, gen, err
}

// walk lists every tool into gen and then ends the walk, waking its waiters.
// A walk that reaches the last page or the page cap lets gen remember misses;
// one that fails lets the next lookup walk again.
func (c *toolCatalog) walk(
	ctx context.Context,
	next mcpsdk.MethodHandler,
	req *mcpsdk.CallToolRequest,
	gen *catalogGeneration,
	walking chan struct{},
) error {
	finished := false
	defer func() {
		c.mu.Lock()
		gen.walking = nil
		if finished {
			gen.walkedAt = c.now()
		}
		c.mu.Unlock()
		close(walking)
	}()
	list := &mcpsdk.ListToolsRequest{Session: req.Session, Params: &mcpsdk.ListToolsParams{}, Extra: req.Extra}
	for range maxListingPages {
		result, err := listThrough(ctx, next, list)
		if err != nil {
			if ctx.Err() != nil {
				// The caller went away; the walk stays retryable and is not a fault.
				return nil
			}
			return err
		}
		page, ok := result.(*mcpsdk.ListToolsResult)
		if !ok {
			return fmt.Errorf("posthogmcpsdk: tools/list through inner handler returned %T", result)
		}
		c.advertise(gen, page.Tools)
		if page.NextCursor == "" {
			finished = true
			return nil
		}
		list.Params = &mcpsdk.ListToolsParams{Cursor: page.NextCursor}
	}
	finished = true
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

// withArguments returns a copy of inputSchema that declares each of arguments
// the schema does not, and the names it added. Schemas whose properties cannot
// be extended safely are left alone. The advertised schema may require an
// argument even under additionalProperties false: go-sdk validates calls
// against the registered schema, and the argument is removed first.
func withArguments(inputSchema any, arguments []analyticsArgument) (map[string]any, []string) {
	schema, properties, ok := extensibleSchema(inputSchema, false)
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
		if argument.required {
			required = append(required, argument.name)
		}
		injected = append(injected, argument.name)
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, injected
}

const instructionsProperty = "_mcp_instructions"

// withInstructions returns outputSchema declaring an optional
// _mcp_instructions object that holds the conversation handle, and whether it
// added it. Otherwise it returns outputSchema unchanged.
func withInstructions(outputSchema any) (any, bool) {
	schema, properties, ok := extensibleSchema(outputSchema, true)
	if !ok {
		return outputSchema, false
	}
	if _, declared := properties[instructionsProperty]; declared {
		return outputSchema, false
	}
	properties[instructionsProperty] = map[string]any{
		"type":        "object",
		"description": "Server-issued metadata for this conversation.",
		"properties": map[string]any{
			conversationArgument: map[string]any{"type": "string", "description": "The server-issued conversation identifier."},
		},
	}
	return schema, true
}

// extensibleSchema decodes a copy of a JSON schema whose properties can be
// extended safely, and its properties: one without $ref, allOf, anyOf, or
// oneOf at its root, whose properties, if any, are an object. An output schema
// must also declare type "object" and no constraint an extra property could
// break (maxProperties, propertyNames), since clients validate delivered
// results against it. An input schema may omit its type, as go-sdk accepts.
func extensibleSchema(raw any, output bool) (schema, properties map[string]any, ok bool) {
	encoded, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(encoded, &schema) != nil || schema == nil {
		return nil, nil, false
	}
	switch schema["type"] {
	case "object":
	case nil:
		if output {
			return nil, nil, false
		}
	default:
		return nil, nil, false
	}
	blockers := []string{"$ref", "allOf", "anyOf", "oneOf"}
	if output {
		blockers = append(blockers, "maxProperties", "propertyNames")
	}
	for _, key := range blockers {
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
