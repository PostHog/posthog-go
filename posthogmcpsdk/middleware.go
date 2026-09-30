package posthogmcpsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcp"
)

const methodCallTool = "tools/call"

// Instrument adds PostHog tool-call analytics to server receiving middleware.
// Install other receiving middleware before or after Instrument according to
// whether its work should be included in the measured duration.
func Instrument(server *mcpsdk.Server, analytics *posthogmcp.Analytics, opts ...Option) {
	server.AddReceivingMiddleware(NewMiddleware(analytics, opts...))
}

// NewMiddleware returns receiving middleware that records terminal tools/call
// requests without changing their result, error, or panic behavior.
func NewMiddleware(analytics *posthogmcp.Analytics, opts ...Option) mcpsdk.Middleware {
	cfg := defaultConfig(analytics)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}

			started := time.Now()
			result, handlerErr := next(ctx, method, req)
			cfg.observeSafely(ctx, req, result, handlerErr, started, time.Since(started))
			return result, handlerErr
		}
	}
}

func (cfg *config) observeSafely(
	ctx context.Context,
	req mcpsdk.Request,
	result mcpsdk.Result,
	handlerErr error,
	started time.Time,
	duration time.Duration,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cfg.report(ctx, fmt.Errorf("posthogmcpsdk: instrumentation panic (%T)", recovered))
		}
	}()
	cfg.observe(ctx, req, result, handlerErr, started, duration)
}

func (cfg *config) observe(
	ctx context.Context,
	req mcpsdk.Request,
	result mcpsdk.Result,
	handlerErr error,
	started time.Time,
	duration time.Duration,
) {
	toolRequest, ok := req.(*mcpsdk.CallToolRequest)
	if !ok || toolRequest == nil || toolRequest.Params == nil {
		cfg.report(ctx, errors.New("posthogmcpsdk: tools/call received an unexpected request type"))
		return
	}

	var toolResult *mcpsdk.CallToolResult
	if result != nil {
		var resultOK bool
		toolResult, resultOK = result.(*mcpsdk.CallToolResult)
		if !resultOK {
			cfg.report(ctx, errors.New("posthogmcpsdk: tools/call returned an unexpected result type"))
			return
		}
	}

	call := posthogmcp.ToolCall{
		ToolName:      toolRequest.Params.Name,
		ServerName:    cfg.serverName,
		ServerVersion: cfg.serverVersion,
		Duration:      duration,
		Timestamp:     started,
	}

	arguments := parseArguments(toolRequest.Params.Arguments)
	if intent := arguments.text(contextArgument); intent != "" {
		call.Intent = intent
		call.IntentSource = posthogmcp.IntentSourceContextParameter
	}
	if cfg.captureParameters {
		call.Parameters = capturedParameters(toolRequest.Params, arguments)
	}
	if cfg.captureResponses && toolResult != nil {
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

	if cfg.identity != nil {
		identity, err := callIdentityResolver(ctx, cfg.identity, toolRequest)
		if err != nil {
			cfg.report(ctx, fmt.Errorf("posthogmcpsdk: identity resolver: %w", err))
		} else {
			call.DistinctID = identity.DistinctID
			call.Groups = identity.Groups
			call.SetProperties = identity.SetProperties
		}
	}
	if cfg.toolMetadata != nil {
		metadata, err := callToolMetadataResolver(ctx, cfg.toolMetadata, toolRequest)
		if err != nil {
			cfg.report(ctx, fmt.Errorf("posthogmcpsdk: tool metadata resolver: %w", err))
		} else {
			call.ToolDescription = metadata.Description
			call.ToolCategory = metadata.Category
		}
	}
	if cfg.properties != nil {
		properties, err := callPropertiesResolver(ctx, cfg.properties, toolRequest, toolResult, handlerErr)
		if err != nil {
			cfg.report(ctx, fmt.Errorf("posthogmcpsdk: properties resolver: %w", err))
		} else {
			call.Properties = properties
		}
	}

	if err := captureToolCall(ctx, cfg.analytics, call); err != nil {
		cfg.report(ctx, fmt.Errorf("posthogmcpsdk: capture: %w", err))
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

func callToolMetadataResolver(
	ctx context.Context,
	resolver ToolMetadataResolver,
	req *mcpsdk.CallToolRequest,
) (metadata ToolMetadata, err error) {
	defer recoverInstrumentationPanic("tool metadata resolver", &err)
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
