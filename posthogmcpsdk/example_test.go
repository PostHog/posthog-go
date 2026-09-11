package posthogmcpsdk_test

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/posthogmcpsdk"
	posthog "github.com/posthog/posthog-go/v2"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

func ExampleInstrument() {
	client := posthog.New("phc_project_api_key")
	defer client.Close()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "weather-server", Version: "1.0.0"}, nil)
	posthogmcpsdk.Instrument(server, posthogmcp.New(client), posthogmcpsdk.WithServerInfo("weather-server", "1.0.0"))
}

func ExampleNewMiddleware() {
	client := posthog.New("phc_project_api_key")
	defer client.Close()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "weather-server", Version: "1.0.0"}, nil)
	middleware := posthogmcpsdk.NewMiddleware(posthogmcp.New(client))
	server.AddReceivingMiddleware(middleware.Receiving)
	server.AddSendingMiddleware(middleware.Sending)
}
