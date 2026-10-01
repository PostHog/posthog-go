package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcp"
)

const (
	methodCallTool              = "tools/call"
	methodListTools             = "tools/list"
	notificationToolListChanged = "notifications/tools/list_changed"
)

// Instrument adds PostHog tool-call analytics to server receiving middleware,
// and the sending middleware described in [Middleware]. Install other
// receiving middleware before or after Instrument according to whether its
// work should be included in the measured duration. It panics if analytics is
// nil.
func Instrument(server *mcpsdk.Server, analytics *posthogmcp.Analytics, opts ...Option) {
	m := NewMiddleware(analytics, opts...)
	server.AddReceivingMiddleware(m.Receiving)
	server.AddSendingMiddleware(m.Sending)
}

// Middleware is what [Instrument] installs, for servers that order their
// middleware by hand. Install Receiving with Server.AddReceivingMiddleware and
// Sending with Server.AddSendingMiddleware.
//
// Receiving records terminal tools/call requests without changing their
// error or panic behavior, or their result beyond the conversation handle
// described at [WithConversationID]. It also adds the enabled analytics
// arguments (context, llm_model, and conversation_id) to tools/list results
// and removes them from tools/call arguments, according to what it learned
// about each tool from tools/list.
//
// Sending forgets what Receiving learned whenever the server sends
// notifications/tools/list_changed, so a tool registered again with a new
// schema is handled by that schema. Without it, the old one still decides
// which arguments are removed. go-sdk sends the notification a few
// milliseconds after a change, and only to connected sessions when the tools
// capability allows it. A tool added or replaced while no session is
// connected is recognized within ten seconds, when Receiving lists tools
// again.
type Middleware struct {
	Receiving mcpsdk.Middleware
	Sending   mcpsdk.Middleware
}

// NewMiddleware returns the [Middleware] [Instrument] installs. It panics if
// analytics is nil.
func NewMiddleware(analytics *posthogmcp.Analytics, opts ...Option) Middleware {
	if analytics == nil {
		panic("posthogmcpsdk: analytics must not be nil")
	}
	cfg := defaultConfig(analytics)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	tools := newToolCatalog(cfg.injectedArguments(), cfg.now)
	m := &middleware{config: cfg, tools: tools, sessions: newSessionResolver(cfg.now)}
	return Middleware{Receiving: m.receive, Sending: m.send}
}

type middleware struct {
	*config
	tools    *toolCatalog
	sessions *sessionResolver
}

func (m *middleware) receive(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		switch method {
		case methodListTools:
			gen := m.tools.generation()
			result, err := next(ctx, method, req)
			if page, ok := result.(*mcpsdk.ListToolsResult); ok && err == nil {
				return m.advertise(ctx, gen, page), nil
			}
			return result, err
		case methodCallTool:
			toolRequest, ok := req.(*mcpsdk.CallToolRequest)
			if !ok || toolRequest == nil || toolRequest.Params == nil {
				m.report(ctx, errors.New("posthogmcpsdk: tools/call received an unexpected request type"))
				return next(ctx, method, req)
			}
			call := m.prepare(ctx, next, toolRequest)
			started := time.Now()
			result, handlerErr := next(ctx, method, call.dispatch)
			delivered := m.deliverConversation(ctx, &call, result)
			m.observeSafely(ctx, call, result, handlerErr, started)
			return delivered, handlerErr
		default:
			return next(ctx, method, req)
		}
	}
}

func (m *middleware) send(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method == notificationToolListChanged {
			// go-sdk sends one change to each connected session in turn, so
			// this runs once per session. Invalidating only swaps in an empty
			// generation, and the sends go out back to back, so the repeats
			// cost at most a tools/list learned in between being learned again.
			m.tools.invalidate()
		}
		return next(ctx, method, req)
	}
}

// preparedCall is a tools/call as instrumentation sees it before dispatch.
type preparedCall struct {
	request      *mcpsdk.CallToolRequest
	dispatch     *mcpsdk.CallToolRequest
	arguments    toolArguments
	tool         toolInfo
	conversation conversation
}

// conversation is the conversation handle of a tools/call, and whether the
// middleware minted it for this call, so the agent has yet to receive it.
type conversation struct {
	id     string
	minted bool
}

var conversationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-7[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// resolveConversation keeps an echoed handle only if it is UUIDv7-shaped,
// since its derived $session_id would otherwise merge every caller that
// invents the same string. Without one, a request that carries no transport
// session gets a new handle.
func resolveConversation(req *mcpsdk.CallToolRequest, arguments toolArguments) conversation {
	if id := arguments.text(conversationArgument); conversationIDPattern.MatchString(id) {
		return conversation{id: strings.ToLower(id)}
	}
	if carriesSession(req) {
		return conversation{}
	}
	return conversation{id: uuid.Must(uuid.NewV7()).String(), minted: true}
}

func (m *middleware) advertise(ctx context.Context, gen *catalogGeneration, page *mcpsdk.ListToolsResult) (advertised *mcpsdk.ListToolsResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: tools/list panic (%T)", recovered))
			advertised = page
		}
	}()
	copied := *page
	copied.Tools = m.tools.advertise(gen, page.Tools)
	return &copied
}

func (m *middleware) prepare(ctx context.Context, next mcpsdk.MethodHandler, req *mcpsdk.CallToolRequest) (call preparedCall) {
	call = preparedCall{request: req, dispatch: req, arguments: parseArguments(req.Params.Arguments)}
	defer func() {
		if recovered := recover(); recovered != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: tools/call preparation panic (%T)", recovered))
			call.dispatch = req
		}
	}()

	tool, err := m.tools.lookup(ctx, next, req)
	if err != nil {
		m.report(ctx, err)
	}
	call.tool = tool
	if tool.injects(conversationArgument) {
		call.conversation = resolveConversation(req, call.arguments)
	}
	if call.arguments.hasAny(tool.injected) {
		arguments, err := json.Marshal(call.arguments.without(tool.injected...))
		if err != nil {
			return call
		}
		params := *req.Params
		params.Arguments = arguments
		dispatch := *req
		dispatch.Params = &params
		call.dispatch = &dispatch
	}
	return call
}

// deliverConversation returns result carrying the call's conversation handle:
// mirrored into structuredContent when the tool's output schema declares
// _mcp_instructions, and appended as a text block when the handle is new. A
// new handle the result cannot carry is forgotten, so no event names a
// conversation the agent never received.
func (m *middleware) deliverConversation(ctx context.Context, call *preparedCall, result mcpsdk.Result) (delivered mcpsdk.Result) {
	handle := call.conversation
	if handle.minted {
		call.conversation = conversation{}
	}
	if handle.id == "" || !(handle.minted || call.tool.instructions) {
		return result
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: conversation delivery panic (%T)", recovered))
			delivered = result
		}
	}()
	toolResult, _ := result.(*mcpsdk.CallToolResult)
	if toolResult == nil || awaitsInput(toolResult) {
		return result
	}
	copied := *toolResult
	if call.tool.instructions {
		copied.StructuredContent = withConversationInstructions(copied.StructuredContent, handle.id)
	}
	if handle.minted {
		block, _ := json.Marshal(map[string]string{conversationArgument: handle.id})
		copied.Content = append(slices.Clip(copied.Content), &mcpsdk.TextContent{Text: string(block)})
	}
	call.conversation = handle
	return &copied
}

// withConversationInstructions returns structured with the conversation
// handle under _mcp_instructions, or unchanged when it is not a JSON object or
// the tool set _mcp_instructions itself.
func withConversationInstructions(structured any, id string) any {
	encoded, err := json.Marshal(structured)
	var object map[string]json.RawMessage
	if err != nil || json.Unmarshal(encoded, &object) != nil || object == nil {
		return structured
	}
	if _, set := object[instructionsProperty]; set {
		return structured
	}
	object[instructionsProperty], _ = json.Marshal(map[string]string{conversationArgument: id})
	return object
}

// awaitsInput reports whether result is an input_required round, which go-sdk
// v1.8 and later return with InputRequests set and no content. The field is
// read by name so the adapter still builds against go-sdk v1.6.1.
func awaitsInput(result *mcpsdk.CallToolResult) bool {
	inputRequests := reflect.ValueOf(result).Elem().FieldByName("InputRequests")
	return inputRequests.IsValid() && !inputRequests.IsZero()
}

func (m *middleware) observeSafely(
	ctx context.Context,
	prepared preparedCall,
	result mcpsdk.Result,
	handlerErr error,
	started time.Time,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: instrumentation panic (%T)", recovered))
		}
	}()
	m.observe(ctx, prepared, result, handlerErr, started)
}

func (m *middleware) observe(
	ctx context.Context,
	prepared preparedCall,
	result mcpsdk.Result,
	handlerErr error,
	started time.Time,
) {
	duration := time.Since(started)
	toolRequest := prepared.request
	var toolResult *mcpsdk.CallToolResult
	if result != nil {
		var resultOK bool
		toolResult, resultOK = result.(*mcpsdk.CallToolResult)
		if !resultOK {
			m.report(ctx, errors.New("posthogmcpsdk: tools/call returned an unexpected result type"))
			return
		}
	}

	call := posthogmcp.ToolCall{
		ToolName:        toolRequest.Params.Name,
		ToolDescription: prepared.tool.description,
		ToolCategory:    prepared.tool.category,
		ServerName:      m.serverName,
		ServerVersion:   m.serverVersion,
		Duration:        duration,
		Timestamp:       started,
	}

	if intent := prepared.arguments.text(contextArgument); intent != "" && m.contextParameter {
		call.Intent = intent
		call.IntentSource = posthogmcp.IntentSourceContextParameter
	}
	if m.captureParameters {
		call.Parameters = capturedParameters(toolRequest.Params, prepared.arguments, prepared.tool.injected)
	}
	if m.captureResponses && toolResult != nil {
		call.Response = toolResult
	}

	if session := toolRequest.Session; session != nil {
		if initialize := session.InitializeParams(); initialize != nil {
			call.ProtocolVersion = initialize.ProtocolVersion
			if initialize.ClientInfo != nil {
				call.ClientName = initialize.ClientInfo.Name
				call.ClientVersion = initialize.ClientInfo.Version
			}
		}
	}

	call.SessionID = m.sessions.resolve(toolRequest.Session, toolRequest.Extra != nil)
	call.ConversationID = prepared.conversation.id

	if m.captureModel {
		call.LLMModel, call.LLMModelSource = callModel(toolRequest.Params.Meta, prepared)
	}
	if extra := toolRequest.Extra; extra != nil {
		call.ClientUserAgent = extra.Header.Get("User-Agent")
		call.VendorClient = extra.Header.Get("X-Anthropic-Client")
	}

	if handlerErr != nil {
		call.Error = handlerErr
	} else if toolResult != nil && toolResult.IsError {
		call.Error = toolResultError(toolResult)
	}

	if m.identity != nil {
		identity, err := callIdentityResolver(ctx, m.identity, toolRequest)
		if err != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: identity resolver: %w", err))
		} else {
			call.DistinctID = identity.DistinctID
			call.Groups = identity.Groups
			call.SetProperties = identity.SetProperties
		}
	}
	if m.properties != nil {
		properties, err := callPropertiesResolver(ctx, m.properties, toolRequest, toolResult, handlerErr)
		if err != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: properties resolver: %w", err))
		} else {
			call.Properties = properties
		}
	}

	if err := captureToolCall(ctx, m.analytics, call); err != nil {
		m.report(ctx, fmt.Errorf("posthogmcpsdk: capture: %w", err))
	}
}

// clientModelMetadata are the request _meta keys whose model field a client
// sets itself, in order of preference.
var clientModelMetadata = []string{"io.modelcontextprotocol/aiInvocation", "x-codex-turn-metadata"}

// callModel is the model the client's metadata names, else the one the agent
// reported in the injected llm_model argument. "unknown" names no model.
func callModel(meta mcpsdk.Meta, prepared preparedCall) (string, posthogmcp.ModelSource) {
	for _, key := range clientModelMetadata {
		metadata, _ := meta[key].(map[string]any)
		if model, _ := metadata["model"].(string); knownModel(model) {
			return model, posthogmcp.ModelSourceClientMetadata
		}
	}
	if model := prepared.arguments.text(modelArgument); prepared.tool.injects(modelArgument) && knownModel(model) {
		return model, posthogmcp.ModelSourceSelfReported
	}
	return "", ""
}

func knownModel(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" && !strings.EqualFold(model, "unknown")
}

// toolResultError is the error behind an isError result: the one a typed
// mcp.AddTool handler returned, which go-sdk keeps on the result, else an error
// of the result's text.
func toolResultError(result *mcpsdk.CallToolResult) error {
	if err := result.GetError(); err != nil {
		return err
	}
	return errors.New(toolResultText(result))
}

func toolResultText(result *mcpsdk.CallToolResult) string {
	var texts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok && text != nil {
			texts = append(texts, text.Text)
		}
	}
	if text := strings.TrimSpace(strings.Join(texts, " ")); text != "" {
		return text
	}
	return "Unknown error"
}

func callIdentityResolver(
	ctx context.Context,
	resolver IdentityResolver,
	req *mcpsdk.CallToolRequest,
) (identity Identity, err error) {
	defer recoverInstrumentationPanic("identity resolver", &err)
	return resolver(ctx, req)
}

func callPropertiesResolver(
	ctx context.Context,
	resolver PropertiesResolver,
	req *mcpsdk.CallToolRequest,
	result *mcpsdk.CallToolResult,
	handlerErr error,
) (properties map[string]any, err error) {
	defer recoverInstrumentationPanic("properties resolver", &err)
	return resolver(ctx, req, result, handlerErr)
}

func captureToolCall(ctx context.Context, analytics *posthogmcp.Analytics, call posthogmcp.ToolCall) (err error) {
	defer recoverInstrumentationPanic("capture", &err)
	return analytics.CaptureToolCall(ctx, call)
}

func recoverInstrumentationPanic(stage string, err *error) {
	if recovered := recover(); recovered != nil {
		*err = fmt.Errorf("%s panic (%T)", stage, recovered)
	}
}

func (cfg *config) report(ctx context.Context, err error) {
	if cfg.errorHandler == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	cfg.errorHandler(ctx, err)
}
