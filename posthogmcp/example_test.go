package posthogmcp_test

import (
	"context"
	"log"
	"time"

	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
)

func ExampleAnalytics_CaptureToolCall() {
	client := posthog.New("phc_project_key")
	defer client.Close()

	analytics := posthogmcp.New(client)
	err := analytics.CaptureToolCall(context.Background(), posthogmcp.ToolCall{
		ToolName:   "search_docs",
		DistinctID: "user_123",
		Duration:   42 * time.Millisecond,
	})
	if err != nil {
		log.Printf("capture MCP analytics: %v", err)
	}
}
