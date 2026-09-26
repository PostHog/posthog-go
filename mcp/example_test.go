package mcp_test

import (
	"log"
	"time"

	posthog "github.com/posthog/posthog-go"
	posthogmcp "github.com/posthog/posthog-go/mcp"
)

func ExampleAnalytics_CaptureToolCall() {
	client := posthog.New("phc_project_key")
	defer client.Close()

	analytics := posthogmcp.New(client)
	err := analytics.CaptureToolCall(posthogmcp.ToolCall{
		ToolName:   "search_docs",
		DistinctID: "user_123",
		Duration:   42 * time.Millisecond,
	})
	if err != nil {
		log.Printf("capture MCP analytics: %v", err)
	}
}
