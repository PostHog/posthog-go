// PostHog Go library examples
//
// This script demonstrates various PostHog Go SDK capabilities including:
// - Basic event capture and user identification
// - Feature flag local evaluation
// - Feature flag payloads
// - Context management
//
// Setup:
// 1. Copy .env.example to .env and fill in your PostHog credentials
// 2. Run this script and choose from the interactive menu

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/posthog/posthog-go" // Used by other files in this package
)

var (
	projectAPIKey string
	secretKey     string
	endpoint      string
)

func init() {
	// Load .env file if it exists (similar to Python SDK)
	_ = godotenv.Load()

	// Get configuration from environment variables
	projectAPIKey = os.Getenv("POSTHOG_PROJECT_API_KEY")
	secretKey = os.Getenv("POSTHOG_SECRET_KEY")
	endpoint = os.Getenv("POSTHOG_ENDPOINT")

	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
}

func promptForInput(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		// If we can't read from stdin (e.g., running in CI), return "6" to run all examples
		fmt.Println("6 (auto-selected for non-interactive environment)")
		return "6"
	}
	input = strings.TrimSpace(input)
	if input == "" {
		// If input is empty (e.g., just pressed enter), return "6" to run all examples
		fmt.Println("6 (auto-selected for empty input)")
		return "6"
	}
	return input
}

func checkCredentials() {
	// Check if credentials are provided
	if projectAPIKey == "" || secretKey == "" {
		fmt.Println("❌ Missing PostHog credentials!")
		fmt.Println("   Please set POSTHOG_PROJECT_API_KEY and POSTHOG_SECRET_KEY environment variables")
		fmt.Println("   or copy .env.example to .env and fill in your values")
		fmt.Println()

		if projectAPIKey == "" {
			projectAPIKey = promptForInput("Enter your PostHog project API key (starts with phc_): ")
		}
		if secretKey == "" {
			secretKey = promptForInput("Enter your PostHog secret API key (starts with phx_): ")
		}
	} else {
		fmt.Println("✅ PostHog credentials loaded successfully!")
		fmt.Println("   Project API Key: [REDACTED]")
		fmt.Println("   Secret API Key: [REDACTED]")
		fmt.Printf("   Endpoint: %s\n\n", endpoint)
	}
}

func showMenu() {
	fmt.Println("🚀 PostHog Go SDK Demo - Choose an example to run:")
	fmt.Println()
	fmt.Println("1. Basic capture examples")
	fmt.Println("2. Capture with feature flags examples")
	fmt.Println("3. Feature flag evaluation examples")
	fmt.Println("4. Feature flag with SendFeatureFlagsOptions examples")
	fmt.Println("5. Flag dependencies examples")
	fmt.Println("6. ETag polling test (continuous, Ctrl+C to stop)")
	fmt.Println("7. Run all examples (except ETag polling)")
	fmt.Println("8. Exit")
}

func runBasicCaptureExamples() {
	printExampleSection("BASIC CAPTURE EXAMPLES")
	TestCapture(projectAPIKey, endpoint)
}

func runCaptureWithFeatureFlagsExamples() {
	printExampleSection("CAPTURE WITH FEATURE FLAGS EXAMPLES")
	TestCaptureWithSendFeatureFlagOption(projectAPIKey, secretKey, endpoint)
}

func runFeatureFlagEvaluationExamples() {
	printExampleSection("FEATURE FLAG EVALUATION EXAMPLES")
	TestIsFeatureEnabled(projectAPIKey, secretKey, endpoint)
}

func runAdvancedFeatureFlagsExamples() {
	printExampleSection("ADVANCED FEATURE FLAGS (SendFeatureFlagsOptions) EXAMPLES")
	TestCaptureWithSendFeatureFlagsOptions(projectAPIKey, secretKey, endpoint)
}

func runFlagDependenciesExamples() {
	printExampleSection("FLAG DEPENDENCIES EXAMPLES")
	TestFlagDependencies(projectAPIKey, secretKey, endpoint)
}

func runETagPollingExample() {
	printExampleSection("ETAG POLLING TEST")
	TestETagPolling(projectAPIKey, secretKey, endpoint)
}

func printExampleSection(title string) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(title)
	fmt.Println(strings.Repeat("=", 60))
}

func runAllExamples() {
	fmt.Println("\n🔄 Running all examples...")

	fmt.Printf("\n%s BASIC CAPTURE %s\n", strings.Repeat("🔸", 20), strings.Repeat("🔸", 20))
	TestCapture(projectAPIKey, endpoint)

	fmt.Printf("\n%s CAPTURE WITH FEATURE FLAGS %s\n", strings.Repeat("🔸", 15), strings.Repeat("🔸", 15))
	TestCaptureWithSendFeatureFlagOption(projectAPIKey, secretKey, endpoint)

	fmt.Printf("\n%s FEATURE FLAG EVALUATION %s\n", strings.Repeat("🔸", 17), strings.Repeat("🔸", 17))
	TestIsFeatureEnabled(projectAPIKey, secretKey, endpoint)
	TestErrorTrackingThroughEnqueueing(projectAPIKey, endpoint)
	TestErrorTrackingThroughLogHandler(projectAPIKey, endpoint)

	fmt.Printf("\n%s ADVANCED FEATURE FLAGS %s\n", strings.Repeat("🔸", 18), strings.Repeat("🔸", 18))
	TestCaptureWithSendFeatureFlagsOptions(projectAPIKey, secretKey, endpoint)

	fmt.Printf("\n%s FLAG DEPENDENCIES %s\n", strings.Repeat("🔸", 20), strings.Repeat("🔸", 20))
	TestFlagDependencies(projectAPIKey, secretKey, endpoint)
}

func isInteractive() bool {
	// Check if we're running in an interactive terminal
	fileInfo, _ := os.Stdin.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

func main() {
	checkCredentials()

	// If not interactive, just run all examples
	if !isInteractive() {
		fmt.Println("🤖 Non-interactive mode detected. Running all examples...")
		runAllExamples()
		return
	}

	for {
		showMenu()
		choice := promptForInput("\nEnter your choice (1-8): ")

		switch choice {
		case "1":
			runBasicCaptureExamples()
		case "2":
			runCaptureWithFeatureFlagsExamples()
		case "3":
			runFeatureFlagEvaluationExamples()
		case "4":
			runAdvancedFeatureFlagsExamples()
		case "5":
			runFlagDependenciesExamples()
		case "6":
			runETagPollingExample()
			// ETag polling runs continuously, so exit after it returns
			return
		case "7":
			runAllExamples()
		case "8":
			fmt.Println("👋 Goodbye!")
			return
		default:
			fmt.Println("❌ Invalid choice. Please select 1-8.")
			continue
		}

		fmt.Println("\n" + strings.Repeat("=", 60))
		fmt.Println("✅ Example completed!")
		fmt.Println(strings.Repeat("=", 60))

		// Ask if user wants to run another example
		again := promptForInput("\nWould you like to run another example? (y/N): ")
		if strings.ToLower(again) != "y" && strings.ToLower(again) != "yes" {
			fmt.Println("👋 Goodbye!")
			break
		}
		fmt.Println()
	}
}
