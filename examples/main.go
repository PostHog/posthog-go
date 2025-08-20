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

	_ "github.com/posthog/posthog-go" // Used by other files in this package
)

var (
	projectAPIKey  string
	personalAPIKey string
	endpoint       string
)

func init() {
	// Load .env file if it exists (similar to Python SDK)
	_ = LoadEnvFile()

	// Get configuration from environment variables
	projectAPIKey = os.Getenv("POSTHOG_PROJECT_API_KEY")
	personalAPIKey = os.Getenv("POSTHOG_PERSONAL_API_KEY")
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
	if projectAPIKey == "" || personalAPIKey == "" {
		fmt.Println("❌ Missing PostHog credentials!")
		fmt.Println("   Please set POSTHOG_PROJECT_API_KEY and POSTHOG_PERSONAL_API_KEY environment variables")
		fmt.Println("   or copy .env.example to .env and fill in your values")
		fmt.Println()
		
		if projectAPIKey == "" {
			projectAPIKey = promptForInput("Enter your PostHog project API key (starts with phc_): ")
		}
		if personalAPIKey == "" {
			personalAPIKey = promptForInput("Enter your PostHog personal API key (starts with phx_): ")
		}
	} else {
		fmt.Println("✅ PostHog credentials loaded successfully!")
		fmt.Printf("   Project API Key: %s...\n", projectAPIKey[:9])
		fmt.Println("   Personal API Key: [REDACTED]")
		fmt.Printf("   Endpoint: %s\n\n", endpoint)
	}
}

func showMenu() {
	fmt.Println("🚀 PostHog Go SDK Demo - Choose an example to run:\n")
	fmt.Println("1. Basic capture examples")
	fmt.Println("2. Capture with feature flags examples")
	fmt.Println("3. Feature flag evaluation examples")
	fmt.Println("4. Feature flag with SendFeatureFlagsOptions examples")
	fmt.Println("5. Flag dependencies examples")
	fmt.Println("6. Run all examples")
	fmt.Println("7. Exit")
}

func runBasicCaptureExamples() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("BASIC CAPTURE EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))
	TestCapture(projectAPIKey, endpoint)
}

func runCaptureWithFeatureFlagsExamples() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("CAPTURE WITH FEATURE FLAGS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))
	TestCaptureWithSendFeatureFlagOption(projectAPIKey, personalAPIKey, endpoint)
}

func runFeatureFlagEvaluationExamples() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("FEATURE FLAG EVALUATION EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))
	TestIsFeatureEnabled(projectAPIKey, personalAPIKey, endpoint)
}

func runAdvancedFeatureFlagsExamples() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("ADVANCED FEATURE FLAGS (SendFeatureFlagsOptions) EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))
	TestCaptureWithSendFeatureFlagsOptions(projectAPIKey, personalAPIKey, endpoint)
}

func runFlagDependenciesExamples() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("FLAG DEPENDENCIES EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))
	TestFlagDependencies(projectAPIKey, personalAPIKey, endpoint)
}

func runAllExamples() {
	fmt.Println("\n🔄 Running all examples...")
	
	fmt.Printf("\n%s BASIC CAPTURE %s\n", strings.Repeat("🔸", 20), strings.Repeat("🔸", 20))
	TestCapture(projectAPIKey, endpoint)
	
	fmt.Printf("\n%s CAPTURE WITH FEATURE FLAGS %s\n", strings.Repeat("🔸", 15), strings.Repeat("🔸", 15))
	TestCaptureWithSendFeatureFlagOption(projectAPIKey, personalAPIKey, endpoint)
	
	fmt.Printf("\n%s FEATURE FLAG EVALUATION %s\n", strings.Repeat("🔸", 17), strings.Repeat("🔸", 17))
	TestIsFeatureEnabled(projectAPIKey, personalAPIKey, endpoint)
	
	fmt.Printf("\n%s ADVANCED FEATURE FLAGS %s\n", strings.Repeat("🔸", 18), strings.Repeat("🔸", 18))
	TestCaptureWithSendFeatureFlagsOptions(projectAPIKey, personalAPIKey, endpoint)
	
	fmt.Printf("\n%s FLAG DEPENDENCIES %s\n", strings.Repeat("🔸", 20), strings.Repeat("🔸", 20))
	TestFlagDependencies(projectAPIKey, personalAPIKey, endpoint)
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
		choice := promptForInput("\nEnter your choice (1-7): ")
		
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
			runAllExamples()
		case "7":
			fmt.Println("👋 Goodbye!")
			return
		default:
			fmt.Println("❌ Invalid choice. Please select 1-7.")
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
