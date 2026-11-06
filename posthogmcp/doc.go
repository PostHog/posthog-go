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
// MCP events carry $lib posthog-go-mcp and $lib_version set to the SDK
// version. ToolCall.Properties, the EventContext's Properties and the request
// context's properties can set either one, key by key like other properties,
// so set both together. A $lib that is not a non-empty string gets the MCP
// pair. BeforeSend can change either.
package posthogmcp
