package posthogmcpgo_test

import (
	"log"

	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/posthogmcpgo/v2"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

func ExampleInstrument() {
	client, err := posthog.New("phc_project_api_key")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	server.NewMCPServer("weather-server", "1.0.0",
		server.WithToolCapabilities(true),
		posthogmcpgo.Instrument(posthogmcp.New(client), posthogmcpgo.WithServerInfo("weather-server", "1.0.0")),
	)
}
