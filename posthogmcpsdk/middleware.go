package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
	if cfg.missingCapabilityTool != "" {
		m.virtual, m.virtualInfo = tools.present(missingCapabilityTool(cfg.missingCapabilityTool))
	}
	return Middleware{Receiving: m.receive, Sending: m.send}
}

type middleware struct {
	*config
	tools       *toolCatalog
	sessions    *sessionResolver
	virtual     *mcpsdk.Tool
	virtualInfo toolInfo
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
			if call.missingCapability {
				// go-sdk answers a name it does not know with its unknown tool
				// error, after the middleware inside Instrument has run.
				if isUnknownTool(handlerErr, toolRequest.Params.Name) {
					result, handlerErr = missingCapabilityResult(), nil
				} else {
					call.missingCapability = false
				}
			}
			delivered := m.deliverConversation(ctx, &call, result, handlerErr)
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
	request           *mcpsdk.CallToolRequest
	dispatch          *mcpsdk.CallToolRequest
	arguments         toolArguments
	tool              toolInfo
	conversation      conversation
	missingCapability bool
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
	if echoed := echoedConversation(arguments); echoed.id != "" {
		return echoed
	}
	if carriesSession(req) {
		return conversation{}
	}
	return conversation{id: uuid.Must(uuid.NewV7()).String(), minted: true}
}

// echoedConversation is the handle the agent sent, if it is UUIDv7-shaped.
func echoedConversation(arguments toolArguments) conversation {
	if id := arguments.text(conversationArgument); conversationIDPattern.MatchString(id) {
		return conversation{id: strings.ToLower(id)}
	}
	return conversation{}
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
	if m.virtual != nil && page.NextCursor == "" && !m.serverRegistersVirtualName(gen, page.Tools) {
		copied.Tools = append(copied.Tools, m.virtual)
	}
	return &copied
}

// The last page is checked itself because the catalog does not learn it when the
// generation was invalidated during the listing.
func (m *middleware) serverRegistersVirtualName(gen *catalogGeneration, lastPage []*mcpsdk.Tool) bool {
	return m.tools.registers(gen, m.missingCapabilityTool) ||
		slices.ContainsFunc(lastPage, func(tool *mcpsdk.Tool) bool { return tool.Name == m.missingCapabilityTool })
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
	if m.virtual != nil && !tool.registered && req.Params.Name == m.missingCapabilityTool {
		call.tool, call.missingCapability = m.virtualInfo, true
	}
	if call.tool.injects(conversationArgument) {
		call.conversation = resolveConversation(req, call.arguments)
	}
	if call.arguments.hasAny(call.tool.injected) {
		arguments, err := json.Marshal(call.arguments.without(call.tool.injected...))
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
// new handle the result cannot carry, or that never reaches the agent because
// go-sdk sends only handlerErr, is forgotten, so no event names a
// conversation the agent never received.
func (m *middleware) deliverConversation(ctx context.Context, call *preparedCall, result mcpsdk.Result, handlerErr error) (delivered mcpsdk.Result) {
	handle := call.conversation
	if handle.minted {
		call.conversation = conversation{}
	}
	if handle.id == "" || handlerErr != nil || !(handle.minted || call.tool.instructions) {
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

// awaitsInput reports whether result is an input_required round the client
// receives, which go-sdk v1.8 and later return for clients of the 2026-07-28
// revision. A round the server fulfils for an older client never reaches
// middleware. The method is asserted by name so the adapter still builds
// against go-sdk v1.6.1.
func awaitsInput(result *mcpsdk.CallToolResult) bool {
	round, ok := any(result).(interface{ NeedsInput() bool })
	return ok && result != nil && round.NeedsInput()
}

// inputRequests is the InputRequests field of result. It is read by name so
// the adapter still builds against go-sdk v1.6.1, which has no such field.
func inputRequests(result *mcpsdk.CallToolResult) reflect.Value {
	if result == nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(result).Elem().FieldByName("InputRequests")
}

// shimExhausted reports whether result still asks for input although the
// client is of a revision that cannot be asked: go-sdk fulfils the requests of
// such a client once and calls the handler again, and a handler that asks again
// ends the call without a final outcome.
func shimExhausted(result *mcpsdk.CallToolResult) bool {
	requests := inputRequests(result)
	return requests.IsValid() && !requests.IsNil() && !awaitsInput(result)
}

// inputRequestMethods is the method of each of the round's input requests,
// ordered by request id. Only the requests are marshalled, never the rest of
// the result.
func inputRequestMethods(result *mcpsdk.CallToolResult) ([]string, error) {
	requests := inputRequests(result)
	if !requests.IsValid() {
		return nil, errors.New("the result has no InputRequests")
	}
	encoded, err := json.Marshal(requests.Interface())
	if err != nil {
		return nil, err
	}
	var wire map[string]struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, err
	}
	methods := make([]string, 0, len(wire))
	for _, id := range slices.Sorted(maps.Keys(wire)) {
		methods = append(methods, wire[id].Method)
	}
	return methods, nil
}

// isUnknownTool reports whether err is go-sdk's answer to a call naming a tool
// the server does not have: its InvalidParams error `unknown tool "<name>"`
// for the requested name. Invalid arguments share the code, not the message,
// and a registered tool that forwards another tool's error names that tool.
func isUnknownTool(err error, name string) bool {
	var rpcErr *jsonrpc.Error
	return errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInvalidParams &&
		rpcErr.Message == fmt.Sprintf("unknown tool %q", name)
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

	unknown := isUnknownTool(handlerErr, toolRequest.Params.Name)
	if unknown && prepared.conversation.id == "" {
		// The catalog has no entry to say the tool carries a handle, but an
		// echoed one still names the conversation. One is never minted.
		prepared.conversation = echoedConversation(prepared.arguments)
	}
	event := m.eventContext(ctx, prepared, started)
	var err error
	switch {
	case prepared.missingCapability:
		err = m.captureMissingCapability(ctx, prepared, event)
	case handlerErr == nil && awaitsInput(toolResult):
		err = m.captureInputRequired(ctx, toolResult, posthogmcp.InputRequired{
			EventContext: event,
			ToolName:     toolRequest.Params.Name,
			Duration:     duration,
		})
	case unknown:
		if strings.TrimSpace(toolRequest.Params.Name) != "" {
			err = capture(ctx, func(ctx context.Context) error {
				return m.analytics.CaptureUnknownTool(ctx, posthogmcp.UnknownTool{EventContext: event, ToolName: toolRequest.Params.Name})
			})
		}
	default:
		err = m.captureToolCall(ctx, prepared, toolResult, handlerErr, event, duration)
	}
	if err != nil {
		m.report(ctx, fmt.Errorf("posthogmcpsdk: capture: %w", err))
	}
}

func (m *middleware) captureMissingCapability(ctx context.Context, prepared preparedCall, event posthogmcp.EventContext) error {
	report := posthogmcp.MissingCapability{
		EventContext: event,
		ToolName:     prepared.request.Params.Name,
		Intent:       prepared.arguments.text(contextArgument),
	}
	if m.captureModel {
		report.LLMModel, report.LLMModelSource = callModel(prepared.request.Params.Meta, prepared)
	}
	return capture(ctx, func(ctx context.Context) error { return m.analytics.CaptureMissingCapability(ctx, report) })
}

func (m *middleware) captureInputRequired(
	ctx context.Context,
	round *mcpsdk.CallToolResult,
	event posthogmcp.InputRequired,
) error {
	methods, err := inputRequestMethods(round)
	if err != nil {
		return fmt.Errorf("input request methods: %w", err)
	}
	event.Methods = methods
	return capture(ctx, func(ctx context.Context) error { return m.analytics.CaptureInputRequired(ctx, event) })
}

// eventContext is what every event of a tools/call carries: the client and
// server it describes, its session, and the caller's identity.
func (m *middleware) eventContext(ctx context.Context, prepared preparedCall, started time.Time) posthogmcp.EventContext {
	toolRequest := prepared.request
	event := posthogmcp.EventContext{
		ServerName:     m.serverName,
		ServerVersion:  m.serverVersion,
		ConversationID: prepared.conversation.id,
		Timestamp:      started,
	}

	if session := toolRequest.Session; session != nil {
		if initialize := session.InitializeParams(); initialize != nil {
			event.ProtocolVersion = initialize.ProtocolVersion
			if initialize.ClientInfo != nil {
				event.ClientName = initialize.ClientInfo.Name
				event.ClientVersion = initialize.ClientInfo.Version
			}
		}
	}

	if event.ConversationID == "" {
		event.SessionID = m.sessions.resolve(toolRequest.Session, !carriesSession(toolRequest))
	}
	if extra := toolRequest.Extra; extra != nil {
		event.ClientUserAgent = extra.Header.Get("User-Agent")
		event.VendorClient = extra.Header.Get("X-Anthropic-Client")
	}

	if m.identity != nil {
		identity, err := callIdentityResolver(ctx, m.identity, toolRequest)
		if err != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: identity resolver: %w", err))
		} else {
			event.DistinctID = identity.DistinctID
			event.Groups = identity.Groups
			event.SetProperties = identity.SetProperties
		}
	}
	return event
}

func (m *middleware) captureToolCall(
	ctx context.Context,
	prepared preparedCall,
	toolResult *mcpsdk.CallToolResult,
	handlerErr error,
	event posthogmcp.EventContext,
	duration time.Duration,
) error {
	toolRequest := prepared.request
	call := posthogmcp.ToolCall{
		ToolName:        toolRequest.Params.Name,
		ToolDescription: prepared.tool.description,
		ToolCategory:    prepared.tool.category,
		DistinctID:      event.DistinctID,
		SessionID:       event.SessionID,
		Groups:          event.Groups,
		SetProperties:   event.SetProperties,
		ServerName:      event.ServerName,
		ServerVersion:   event.ServerVersion,
		ClientName:      event.ClientName,
		ClientVersion:   event.ClientVersion,
		ProtocolVersion: event.ProtocolVersion,
		ConversationID:  event.ConversationID,
		ClientUserAgent: event.ClientUserAgent,
		VendorClient:    event.VendorClient,
		Duration:        duration,
		Timestamp:       event.Timestamp,
	}

	call.Intent, call.IntentSource = m.callIntent(ctx, prepared)
	if m.captureParameters {
		call.Parameters = capturedParameters(toolRequest.Params, prepared.arguments, prepared.tool.injected)
	}
	if m.captureResponses && toolResult != nil {
		call.Response = toolResult
	}
	if m.captureModel {
		call.LLMModel, call.LLMModelSource = callModel(toolRequest.Params.Meta, prepared)
	}

	if handlerErr != nil {
		call.Error = handlerErr
	} else if toolResult != nil && toolResult.IsError {
		call.Error = toolResultError(toolResult)
	} else if shimExhausted(toolResult) {
		call.Error = errors.New("the handler still asked for input after go-sdk ran it again with the answers")
		call.ErrorType = errorTypeInputRequired
	}

	if m.properties != nil {
		properties, err := callPropertiesResolver(ctx, m.properties, toolRequest, toolResult, handlerErr)
		if err != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: properties resolver: %w", err))
		} else {
			call.Properties = properties
		}
	}

	return capture(ctx, func(ctx context.Context) error { return m.analytics.CaptureToolCall(ctx, call) })
}

// callIntent is the intent the agent stated in the context argument, else the
// one the fallback infers.
func (m *middleware) callIntent(ctx context.Context, prepared preparedCall) (string, posthogmcp.IntentSource) {
	if intent := prepared.arguments.text(contextArgument); intent != "" && m.contextParameter {
		return intent, posthogmcp.IntentSourceContextParameter
	}
	if m.intentFallback == nil {
		return "", ""
	}
	intent, err := callIntentFallback(ctx, m.intentFallback, prepared.request)
	if err != nil {
		m.report(ctx, fmt.Errorf("posthogmcpsdk: intent fallback: %w", err))
		return "", ""
	}
	return intent, posthogmcp.IntentSourceInferred
}

// errorTypeInputRequired is the $mcp_error_type of a call whose input_required
// rounds ended without a final outcome.
const errorTypeInputRequired = "input_required"

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

func callIntentFallback(
	ctx context.Context,
	fallback IntentFallback,
	req *mcpsdk.CallToolRequest,
) (intent string, err error) {
	defer recoverInstrumentationPanic("intent fallback", &err)
	return fallback(ctx, req)
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

func capture(ctx context.Context, send func(context.Context) error) (err error) {
	defer recoverInstrumentationPanic("capture", &err)
	return send(ctx)
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
