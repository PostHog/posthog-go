// Package mcp builds and enqueues canonical PostHog analytics events for Model
// Context Protocol activity without depending on a particular MCP framework.
//
// Applications own the PostHog client lifecycle and pass completed tool calls
// to Analytics. Tool parameters, responses, intent, and error messages are
// sanitized and bounded before they are queued.
package mcp
