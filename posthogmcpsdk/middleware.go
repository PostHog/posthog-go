package posthogmcpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
// result, error, or panic behavior. Unless WithContextParameter(false) is set,
// it also adds the context argument to tools/list results and removes it from
// tools/call arguments, according to what it learned about each tool from
// tools/list.
//
// Sending forgets what Receiving learned whenever the server sends
// notifications/tools/list_changed, so a tool registered again with a new
// schema is handled by that schema. Without it, the old one still decides
// whether context is removed. go-sdk sends the notification a few
// milliseconds after a change, and only to connected sessions when the tools
// capability allows it. A tool replaced while no session is connected keeps
// its old entry until a tools/list result includes it again.
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
	m := &middleware{config: cfg, tools: newToolCatalog(cfg.contextParameter)}
	return Middleware{Receiving: m.receive, Sending: m.send}
}

type middleware struct {
	*config
	tools *toolCatalog
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
			m.observeSafely(ctx, call, result, handlerErr, started)
			return result, handlerErr
		default:
			return next(ctx, method, req)
		}
	}
}

func (m *middleware) send(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method == notificationToolListChanged {
			m.tools.invalidate()
		}
		return next(ctx, method, req)
	}
}

// preparedCall is a tools/call as instrumentation sees it before dispatch.
type preparedCall struct {
	request   *mcpsdk.CallToolRequest
	dispatch  *mcpsdk.CallToolRequest
	arguments toolArguments
	tool      toolInfo
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
	if _, sent := call.arguments[contextArgument]; sent && call.tool.contextInjected {
		arguments, err := json.Marshal(call.arguments.without(contextArgument))
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

func (m *middleware) observeSafely(
	ctx context.Context,
	call preparedCall,
	result mcpsdk.Result,
	handlerErr error,
	started time.Time,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.report(ctx, fmt.Errorf("posthogmcpsdk: instrumentation panic (%T)", recovered))
		}
	}()
	m.observe(ctx, call, result, handlerErr, started)
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
		call.Parameters = capturedParameters(toolRequest.Params, prepared.arguments)
	}
	if m.captureResponses && toolResult != nil {
		call.Response = toolResult
	}

	if session := toolRequest.Session; session != nil {
		call.SessionID = session.ID()
		if initialize := session.InitializeParams(); initialize != nil {
			call.ProtocolVersion = initialize.ProtocolVersion
			if initialize.ClientInfo != nil {
				call.ClientName = initialize.ClientInfo.Name
				call.ClientVersion = initialize.ClientInfo.Version
			}
		}
	}

	// TODO: once ToolCall has the conversation, model, and transport fields,
	// set LLMModel from toolRequest.Params.Meta["x-codex-turn-metadata"]["model"]
	// with ModelSourceClientMetadata, ClientUserAgent from the User-Agent header
	// in toolRequest.Extra, and VendorClient from its X-Anthropic-Client header.

	if handlerErr != nil {
		call.Error = handlerErr
	} else if toolResult != nil && toolResult.IsError {
		call.Error = errors.New(toolResultText(toolResult))
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

func toolResultText(result *mcpsdk.CallToolResult) string {
	var texts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok {
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
