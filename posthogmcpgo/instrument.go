package posthogmcpgo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/posthogmcp"
)

// Instrument returns a server option that adds PostHog tool-call analytics to
// an mcp-go server. It installs a tool filter that advertises the analytics
// arguments on listed tools, a tool handler middleware that removes them from
// calls before the handler, and call hooks that capture each call with the
// result the client receives, including calls rejected by input or output
// schema validation. The hooks are added to the server's existing hooks, so
// pass Instrument after server.WithHooks: a WithHooks given later replaces
// them, and the middleware then captures calls instead, without validation
// failures. Middleware runs in the order its options are given, outermost
// first, so middleware given before Instrument sees the analytics arguments
// and middleware given after it does not. It panics if analytics is nil.
func Instrument(analytics *posthogmcp.Analytics, opts ...Option) server.ServerOption {
	if analytics == nil {
		panic("posthogmcpgo: analytics must not be nil")
	}
	cfg := defaultConfig(analytics)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return func(s *server.MCPServer) {
		in := &instrumentation{
			config:    cfg,
			server:    s,
			inject:    cfg.injectedArguments(),
			sessions:  newSessionResolver(),
			protocols: newProtocolVersions(),
			calls:     newCallsInFlight(),
		}
		server.WithToolFilter(in.advertise)(s)
		server.WithToolHandlerMiddleware(in.middleware)(s)
		in.hooks = s.GetHooks()
		if in.hooks == nil {
			in.hooks = &server.Hooks{}
			server.WithHooks(in.hooks)(s)
		}
		in.hooks.AddAfterInitialize(in.initialized)
		in.hooks.AddOnUnregisterSession(in.unregistered)
		in.hooks.AddBeforeCallTool(in.beforeCall)
		in.hooks.AddAfterCallTool(in.afterCall)
		in.hooks.AddOnError(in.callFailed)
	}
}

type instrumentation struct {
	*config
	server    *server.MCPServer
	hooks     *server.Hooks
	inject    []analyticsArgument
	sessions  *sessionResolver
	protocols *protocolVersions
	calls     *callsInFlight
	// hooksReplaced reports once that the server's hooks are no longer the
	// ones Instrument added to.
	hooksReplaced sync.Once
}

// advertise runs as a tool filter on tools/list and on each tools/call, where
// mcp-go only checks that the called tool's name survives it. Task tools,
// whose calls never run tool handler middleware, are listed unchanged.
func (in *instrumentation) advertise(ctx context.Context, tools []mcp.Tool) (advertised []mcp.Tool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: tool filter panic (%T)", recovered))
			advertised = tools
		}
	}()
	advertised = make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		advertised[i] = tool
		if _, wrapped := in.registeredTool(ctx, tool.Name); wrapped {
			advertised[i], _ = present(tool, in.inject)
		}
	}
	return advertised
}

func (in *instrumentation) middleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dispatch := in.strip(ctx, req)
		if !in.capturesInMiddleware(ctx, req) {
			return next(ctx, dispatch)
		}
		started := callStart{at: time.Now(), protocolVersion: in.protocols.of(server.ClientSessionFromContext(ctx))}
		result, err := next(ctx, dispatch)
		in.observe(ctx, req, result, err, started)
		return result, err
	}
}

// strip returns req without the analytics arguments the called tool was
// advertised with.
func (in *instrumentation) strip(ctx context.Context, req mcp.CallToolRequest) (dispatch mcp.CallToolRequest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: tools/call preparation panic (%T)", recovered))
			dispatch = req
		}
	}()
	tool, _ := in.registeredTool(ctx, req.Params.Name)
	_, info := present(tool, in.inject)
	arguments := parseArguments(rawArguments(req.Params))
	if !arguments.hasAny(info.injected) {
		return req
	}
	dispatch, err := withoutArguments(req, arguments, info.injected)
	if err != nil {
		in.report(ctx, fmt.Errorf("posthogmcpgo: remove analytics arguments: %w", err))
		return req
	}
	return dispatch
}

// capturesInMiddleware is whether the middleware, rather than the call hooks,
// captures this call: when the call runs as a task, whose hooks see the task
// rather than the tool's result, or when the server's hooks were replaced
// after Instrument added to them.
func (in *instrumentation) capturesInMiddleware(ctx context.Context, req mcp.CallToolRequest) bool {
	if tool, ok := in.registeredTool(ctx, req.Params.Name); ok && runsAsTask(tool, req) {
		return true
	}
	if in.server.GetHooks() == in.hooks {
		return false
	}
	in.hooksReplaced.Do(func() {
		in.report(ctx, errors.New("posthogmcpgo: server.WithHooks replaced the hooks Instrument added to; "+
			"pass Instrument after server.WithHooks. Capturing tool calls in middleware, without validation failures"))
	})
	return true
}

// runsAsTask mirrors mcp-go's choice to run a tools/call as a task.
func runsAsTask(tool mcp.Tool, req mcp.CallToolRequest) bool {
	return req.Params.Task != nil && tool.Execution != nil &&
		(tool.Execution.TaskSupport == mcp.TaskSupportOptional || tool.Execution.TaskSupport == mcp.TaskSupportRequired)
}

// registeredTool is the tool a call reaches through tool handler middleware:
// the session's own tool of that name, else the server's, as mcp-go resolves
// it. Task tools are not among them.
func (in *instrumentation) registeredTool(ctx context.Context, name string) (mcp.Tool, bool) {
	if session, ok := server.ClientSessionFromContext(ctx).(server.SessionWithTools); ok {
		if tool, ok := session.GetSessionTools()[name]; ok {
			return tool.Tool, true
		}
	}
	if tool := in.server.GetTool(name); tool != nil {
		return tool.Tool, true
	}
	return mcp.Tool{}, false
}

// ours is whether a hook fired for this server's request. Servers can share
// one *server.Hooks, and each instrumented server adds its hooks to it.
func (in *instrumentation) ours(ctx context.Context) bool {
	return server.ServerFromContext(ctx) == in.server
}

func (in *instrumentation) initialized(ctx context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
	defer in.recoverHook(ctx, "after initialize")
	if result != nil && in.ours(ctx) {
		in.protocols.record(server.ClientSessionFromContext(ctx), result.ProtocolVersion)
	}
}

func (in *instrumentation) unregistered(ctx context.Context, session server.ClientSession) {
	defer in.recoverHook(ctx, "unregister session")
	in.protocols.forget(session)
	in.sessions.forget(session)
}

// beforeCall starts tracking a call to a tool the middleware wraps, so calls to
// unknown tools and task tools are never captured.
func (in *instrumentation) beforeCall(ctx context.Context, _ any, req *mcp.CallToolRequest) {
	defer in.recoverHook(ctx, "before tools/call")
	if _, wrapped := in.registeredTool(ctx, req.Params.Name); !wrapped || !in.ours(ctx) {
		return
	}
	in.calls.start(req, callStart{at: time.Now(), protocolVersion: in.protocols.of(server.ClientSessionFromContext(ctx))})
}

// afterCall captures a call with the result mcp-go sends, after input and
// output schema validation. A task-augmented call's result is the task, so
// the middleware captures it instead.
func (in *instrumentation) afterCall(ctx context.Context, _ any, req *mcp.CallToolRequest, result any) {
	defer in.recoverHook(ctx, "after tools/call")
	started, ok := in.calls.finish(req)
	if toolResult, isToolResult := result.(*mcp.CallToolResult); ok && isToolResult {
		in.observe(ctx, *req, toolResult, nil, started)
	}
}

// callFailed captures a tools/call answered with a JSON-RPC error, such as a
// handler's returned error. A call to an unknown tool is not a tool call, as
// in posthogmcpsdk, and a request mcp-go cannot parse never started one.
func (in *instrumentation) callFailed(ctx context.Context, _ any, method mcp.MCPMethod, message any, err error) {
	defer in.recoverHook(ctx, "tools/call error")
	req, isCall := message.(*mcp.CallToolRequest)
	if method != mcp.MethodToolsCall || !isCall {
		return
	}
	started, ok := in.calls.finish(req)
	if ok && !errors.Is(err, server.ErrToolNotFound) {
		// mcp-go wraps the handler's error in its own request error.
		if handlerErr := errors.Unwrap(err); handlerErr != nil {
			err = handlerErr
		}
		in.observe(ctx, *req, nil, err, started)
	}
}

func (in *instrumentation) recoverHook(ctx context.Context, hook string) {
	if recovered := recover(); recovered != nil {
		in.report(ctx, fmt.Errorf("posthogmcpgo: %s hook panic (%T)", hook, recovered))
	}
}

// observe captures a call. req is the request as the client sent it,
// including the analytics arguments.
func (in *instrumentation) observe(
	ctx context.Context,
	req mcp.CallToolRequest,
	result *mcp.CallToolResult,
	handlerErr error,
	started callStart,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: instrumentation panic (%T)", recovered))
		}
	}()
	if err := in.captureToolCall(ctx, req, result, handlerErr, started); err != nil {
		in.report(ctx, fmt.Errorf("posthogmcpgo: capture: %w", err))
	}
}

func (in *instrumentation) captureToolCall(
	ctx context.Context,
	req mcp.CallToolRequest,
	result *mcp.CallToolResult,
	handlerErr error,
	started callStart,
) error {
	session := server.ClientSessionFromContext(ctx)
	tool, _ := in.registeredTool(ctx, req.Params.Name)
	_, info := present(tool, in.inject)
	raw := rawArguments(req.Params)
	arguments := parseArguments(raw)
	call := posthogmcp.ToolCall{
		ToolName:        req.Params.Name,
		ToolDescription: info.description,
		ToolCategory:    info.category,
		SessionID:       in.sessions.resolve(session, time.Now()),
		ServerName:      in.serverName,
		ServerVersion:   in.serverVersion,
		ProtocolVersion: cmp.Or(req.Header.Get("Mcp-Protocol-Version"), started.protocolVersion),
		ClientUserAgent: req.Header.Get("User-Agent"),
		VendorClient:    req.Header.Get("X-Anthropic-Client"),
		Duration:        time.Since(started.at),
		Timestamp:       started.at,
	}
	if session, ok := session.(server.SessionWithClientInfo); ok {
		client := session.GetClientInfo()
		call.ClientName, call.ClientVersion = client.Name, client.Version
	}

	if intent := arguments.text(contextArgument); in.contextParameter && intent != "" && intent != emptyJSONObject {
		call.Intent, call.IntentSource = intent, posthogmcp.IntentSourceContextParameter
	}
	if in.captureParameters {
		call.Parameters = capturedParameters(req.Params.Name, raw, arguments, info.injected)
	}
	// mcp-go answers a handler error alone and drops the result.
	if in.captureResponses && result != nil && handlerErr == nil {
		call.Response = result
	}
	if in.captureModel {
		call.LLMModel, call.LLMModelSource = callModel(req.Params.Meta, arguments, info)
	}

	if handlerErr != nil {
		call.Error = handlerErr
	} else if result != nil && result.IsError {
		call.Error = errors.New(toolResultText(result))
	}

	if in.identity != nil {
		identity, err := callIdentityResolver(ctx, in.identity, req)
		if err != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: identity resolver: %w", err))
		} else {
			call.DistinctID, call.Groups, call.SetProperties = identity.DistinctID, identity.Groups, identity.SetProperties
		}
	}
	if in.properties != nil {
		properties, err := callPropertiesResolver(ctx, in.properties, req, result, handlerErr)
		if err != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: properties resolver: %w", err))
		} else {
			call.Properties = properties
		}
	}

	return capture(ctx, func(ctx context.Context) error { return in.analytics.CaptureToolCall(ctx, call) })
}

// clientModelMetadata are the request _meta keys whose model field a client
// sets itself, in order of preference.
var clientModelMetadata = []string{"io.modelcontextprotocol/aiInvocation", "x-codex-turn-metadata"}

// callModel is the model the client's metadata names, else the one the agent
// reported in the injected llm_model argument. "unknown" names no model.
func callModel(meta *mcp.Meta, arguments toolArguments, tool toolInfo) (string, posthogmcp.ModelSource) {
	if meta != nil {
		for _, key := range clientModelMetadata {
			metadata, _ := meta.AdditionalFields[key].(map[string]any)
			if model, _ := metadata["model"].(string); knownModel(model) {
				return model, posthogmcp.ModelSourceClientMetadata
			}
		}
	}
	if model := arguments.text(modelArgument); tool.injects(modelArgument) && knownModel(model) {
		return model, posthogmcp.ModelSourceSelfReported
	}
	return "", ""
}

func (info toolInfo) injects(argument string) bool {
	return slices.Contains(info.injected, argument)
}

func knownModel(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" && !strings.EqualFold(model, "unknown")
}

func toolResultText(result *mcp.CallToolResult) string {
	var texts []string
	for _, content := range result.Content {
		if text, ok := mcp.AsTextContent(content); ok {
			texts = append(texts, text.Text)
		}
	}
	if text := strings.TrimSpace(strings.Join(texts, " ")); text != "" {
		return text
	}
	return "Unknown error"
}

func callIdentityResolver(ctx context.Context, resolver IdentityResolver, req mcp.CallToolRequest) (identity Identity, err error) {
	defer recoverInstrumentationPanic("identity resolver", &err)
	return resolver(ctx, req)
}

func callPropertiesResolver(
	ctx context.Context,
	resolver PropertiesResolver,
	req mcp.CallToolRequest,
	result *mcp.CallToolResult,
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
