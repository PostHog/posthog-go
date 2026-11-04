package posthogmcpgo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/posthogmcp"
)

// Instrument returns a server option that adds PostHog tool-call analytics to
// an mcp-go server: a tool filter that advertises the analytics arguments on
// listed tools, and a tool handler middleware that removes them from calls
// and captures each call. Pass it to server.NewMCPServer with the server's
// other options. Middleware runs in the order its options are given,
// outermost first, so place Instrument before or after other tool handler
// middleware according to whether its work should be included in the
// measured duration. It panics if analytics is nil.
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
		in := &instrumentation{config: cfg, server: s, inject: cfg.injectedArguments(), sessions: newSessionResolver()}
		server.WithToolFilter(in.advertise)(s)
		server.WithToolHandlerMiddleware(in.middleware)(s)
	}
}

type instrumentation struct {
	*config
	server   *server.MCPServer
	inject   []analyticsArgument
	sessions *sessionResolver
}

// advertise runs as a tool filter on tools/list and on each tools/call, where
// mcp-go only checks that the called tool's name survives it.
func (in *instrumentation) advertise(ctx context.Context, tools []mcp.Tool) (advertised []mcp.Tool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: tool filter panic (%T)", recovered))
			advertised = tools
		}
	}()
	advertised = make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		advertised[i], _ = present(tool, in.inject)
	}
	return advertised
}

func (in *instrumentation) middleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		call := in.prepare(ctx, req)
		started := time.Now()
		result, err := next(ctx, call.dispatch)
		in.observeSafely(ctx, call, result, err, started)
		return result, err
	}
}

// preparedCall is a tools/call as instrumentation sees it before dispatch.
type preparedCall struct {
	request   mcp.CallToolRequest
	dispatch  mcp.CallToolRequest
	raw       []byte
	arguments toolArguments
	tool      toolInfo
	session   string
}

func (in *instrumentation) prepare(ctx context.Context, req mcp.CallToolRequest) (call preparedCall) {
	call = preparedCall{request: req, dispatch: req}
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: tools/call preparation panic (%T)", recovered))
			call.dispatch = req
		}
	}()
	call.session = in.sessions.resolve(server.ClientSessionFromContext(ctx), time.Now())
	call.raw = rawArguments(req.Params)
	call.arguments = parseArguments(call.raw)
	if tool, ok := in.registeredTool(ctx, req.Params.Name); ok {
		_, call.tool = present(tool, in.inject)
	}
	if call.arguments.hasAny(call.tool.injected) {
		dispatch, err := withoutArguments(req, call.arguments, call.tool.injected)
		if err != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: remove analytics arguments: %w", err))
			return call
		}
		call.dispatch = dispatch
	}
	return call
}

// registeredTool is the tool a call reaches: the session's own tool of that
// name, else the server's, as mcp-go resolves it.
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

func (in *instrumentation) observeSafely(
	ctx context.Context,
	prepared preparedCall,
	result *mcp.CallToolResult,
	handlerErr error,
	started time.Time,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			in.report(ctx, fmt.Errorf("posthogmcpgo: instrumentation panic (%T)", recovered))
		}
	}()
	if err := in.captureToolCall(ctx, prepared, result, handlerErr, started); err != nil {
		in.report(ctx, fmt.Errorf("posthogmcpgo: capture: %w", err))
	}
}

func (in *instrumentation) captureToolCall(
	ctx context.Context,
	prepared preparedCall,
	result *mcp.CallToolResult,
	handlerErr error,
	started time.Time,
) error {
	req := prepared.request
	call := posthogmcp.ToolCall{
		ToolName:        req.Params.Name,
		ToolDescription: prepared.tool.description,
		ToolCategory:    prepared.tool.category,
		SessionID:       prepared.session,
		ServerName:      in.serverName,
		ServerVersion:   in.serverVersion,
		ProtocolVersion: req.Header.Get("Mcp-Protocol-Version"),
		ClientUserAgent: req.Header.Get("User-Agent"),
		VendorClient:    req.Header.Get("X-Anthropic-Client"),
		Duration:        time.Since(started),
		Timestamp:       started,
	}
	if session, ok := server.ClientSessionFromContext(ctx).(server.SessionWithClientInfo); ok {
		client := session.GetClientInfo()
		call.ClientName, call.ClientVersion = client.Name, client.Version
	}

	if intent := prepared.arguments.text(contextArgument); in.contextParameter && intent != "" && intent != emptyJSONObject {
		call.Intent, call.IntentSource = intent, posthogmcp.IntentSourceContextParameter
	}
	if in.captureParameters {
		call.Parameters = capturedParameters(req.Params.Name, prepared.raw, prepared.arguments, prepared.tool.injected)
	}
	// mcp-go answers a handler error alone and drops the result.
	if in.captureResponses && result != nil && handlerErr == nil {
		call.Response = result
	}
	if in.captureModel {
		call.LLMModel, call.LLMModelSource = callModel(req.Params.Meta, prepared)
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
func callModel(meta *mcp.Meta, prepared preparedCall) (string, posthogmcp.ModelSource) {
	if meta != nil {
		for _, key := range clientModelMetadata {
			metadata, _ := meta.AdditionalFields[key].(map[string]any)
			if model, _ := metadata["model"].(string); knownModel(model) {
				return model, posthogmcp.ModelSourceClientMetadata
			}
		}
	}
	if model := prepared.arguments.text(modelArgument); prepared.tool.injects(modelArgument) && knownModel(model) {
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
