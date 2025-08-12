package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var (
	projectAPIKey  = os.Getenv("POSTHOG_PROJECT_API_KEY")
	personalAPIKey = os.Getenv("POSTHOG_PERSONAL_API_KEY")
	endpoint       = os.Getenv("POSTHOG_ENDPOINT")
)

func init() {
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
}

func promptForInput(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func main() {
	if projectAPIKey == "" {
		projectAPIKey = promptForInput("Enter your PostHog project API key (starts with phc_): ")
	}
	if personalAPIKey == "" {
		personalAPIKey = promptForInput("Enter your PostHog personal API key (starts with phx_): ")
	}

	TestCapture(projectAPIKey, endpoint)
	TestCaptureWithSendFeatureFlagOption(projectAPIKey, personalAPIKey, endpoint)
	TestIsFeatureEnabled(projectAPIKey, personalAPIKey, endpoint)
	TestErrorTrackingThroughEnqueueing(projectAPIKey, endpoint)
	TestErrorTrackingThroughLogHandler(projectAPIKey, endpoint)
}
