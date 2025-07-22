package main

import (
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

func TestCapture(projectAPIKey, endpoint string) {
	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
		Endpoint:  endpoint,
	})
	defer client.Close()

	done := time.After(3 * time.Second)
	tick := time.Tick(50 * time.Millisecond)

	for {
		select {
		case <-done:
			fmt.Println("exiting")
			return

		case <-tick:
			if err := client.Enqueue(posthog.Capture{
				Event:      "Download",
				DistinctId: "123456",
				Properties: map[string]interface{}{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos", // :)
				},
			}); err != nil {
				fmt.Println("error:", err)
				return
			}
		}
	}
}

func TestCaptureWithSendFeatureFlagOption(projectAPIKey, personalAPIKey, endpoint string) {
	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:       30 * time.Second,
		BatchSize:      100,
		Verbose:        true,
		PersonalApiKey: personalAPIKey,
		Endpoint:       endpoint,
	})
	defer client.Close()

	done := time.After(3 * time.Second)
	tick := time.Tick(50 * time.Millisecond)

	for {
		select {
		case <-done:
			fmt.Println("exiting")
			return

		case <-tick:
			if err := client.Enqueue(posthog.Capture{
				Event:      "Download",
				DistinctId: "123456",
				Properties: map[string]interface{}{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos", // :)
				},
				SendFeatureFlags: true,
			}); err != nil {
				fmt.Println("error:", err)
				return
			}
		}
	}
}

func TestCaptureWithSendFeatureFlagsOptions(projectAPIKey, personalAPIKey, endpoint string) {
	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:       30 * time.Second,
		BatchSize:      100,
		Verbose:        true,
		PersonalApiKey: personalAPIKey,
		Endpoint:       endpoint,
	})
	defer client.Close()

	done := time.After(3 * time.Second)
	tick := time.Tick(50 * time.Millisecond)

	for {
		select {
		case <-done:
			fmt.Println("exiting")
			return

		case <-tick:
			// Example 1: Using the new SendFeatureFlagsOptions with person properties
			if err := client.Enqueue(posthog.Capture{
				Event:      "Download",
				DistinctId: "123456",
				Properties: map[string]interface{}{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos",
				},
				SendFeatureFlags: &posthog.SendFeatureFlagsOptions{
					PersonProperties: posthog.NewProperties().Set("plan", "premium").Set("beta_user", true),
				},
			}); err != nil {
				fmt.Println("error:", err)
				return
			}

			// Example 2: Using SendFeatureFlagsOptions with local-only evaluation
			if err := client.Enqueue(posthog.Capture{
				Event:      "Purchase",
				DistinctId: "123456",
				Properties: map[string]interface{}{
					"amount": 99.99,
					"currency": "USD",
				},
				SendFeatureFlags: &posthog.SendFeatureFlagsOptions{
					OnlyEvaluateLocally: true,
					PersonProperties:    posthog.NewProperties().Set("plan", "premium"),
					GroupProperties: map[string]posthog.Properties{
						"company": posthog.NewProperties().Set("name", "PostHog").Set("plan", "enterprise"),
					},
				},
			}); err != nil {
				fmt.Println("error:", err)
				return
			}
		}
	}
}
