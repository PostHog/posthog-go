package posthogmcpsdk_test

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	posthog "github.com/posthog/posthog-go"
	posthogmcpsdk "github.com/posthog/posthog-go/contrib/modelcontextprotocol/go-sdk"
	posthogmcp "github.com/posthog/posthog-go/mcp"
)

func ExampleInstrument() {
	posthogClient, _ := posthog.NewWithConfig("phc_project_api_key", posthog.Config{
		Endpoint: "https://us.i.posthog.com",
	})
	defer posthogClient.Close()

	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "weather-server", Version: "1.0.0"},
		nil,
	)
	analytics := posthogmcp.New(posthogClient)
	posthogmcpsdk.Instrument(server, analytics,
		posthogmcpsdk.WithServerInfo("weather-server", "1.0.0"),
		posthogmcpsdk.WithCaptureParameters(false),
		posthogmcpsdk.WithCaptureResponses(false),
	)
}
