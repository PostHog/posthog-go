// Package posthogmcp builds and enqueues canonical PostHog analytics events for Model
// Context Protocol activity without depending on a particular MCP framework.
//
// Applications own the PostHog client lifecycle and pass completed tool calls
// to Analytics. Tool parameters, responses, intent, and error messages are
// sanitized and bounded before they are queued. A call that names an
// unregistered tool, and an input_required round the client receives, are not
// tool calls: [Analytics.CaptureUnknownTool] and [Analytics.CaptureInputRequired]
// send their own events. Parameter or response JSON
// exceeding 1 MiB after media redaction is replaced with an omission marker.
//
// MCP events carry $lib posthog-go-mcp. A client in CaptureModeAnalyticsV1 sends
// one library name per request, so there its MCP events report posthog-go;
// create a dedicated client for MCP analytics to keep the two apart.
package posthogmcp
