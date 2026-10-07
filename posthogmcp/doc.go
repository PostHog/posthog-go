// Package posthogmcp builds and enqueues canonical PostHog analytics events for Model
// Context Protocol activity without depending on a particular MCP framework.
//
// Applications own the PostHog client lifecycle and pass completed tool calls
// to Analytics. Tool parameters, responses, intent, and error messages are
// sanitized and bounded before they are queued. A call that names an
// unregistered tool, an input_required round the client receives, and a report
// of a missing capability, are not tool calls: [Analytics.CaptureUnknownTool],
// [Analytics.CaptureInputRequired], and [Analytics.CaptureMissingCapability]
// send their own events. Parameter or response JSON
// exceeding 1 MiB after media redaction is replaced with an omission marker.
//
// MCP events carry $lib posthog-go-mcp in the properties that BeforeSend sees.
// The server takes $lib from the per-request PostHog-Sdk-Info header, so stored
// MCP events report posthog-go.
package posthogmcp
