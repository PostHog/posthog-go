package posthogmcpgo_test

import (
	"github.com/mark3labs/mcp-go/server"
	posthog "github.com/posthog/posthog-go"
	"github.com/posthog/posthog-go/posthogmcp"
	"github.com/posthog/posthog-go/posthogmcpgo"
)

func ExampleInstrument() {
	client := posthog.New("phc_project_api_key")
	defer client.Close()

	server.NewMCPServer("weather-server", "1.0.0",
		server.WithToolCapabilities(true),
		posthogmcpgo.Instrument(posthogmcp.New(client), posthogmcpgo.WithServerInfo("weather-server", "1.0.0")),
	)
}
