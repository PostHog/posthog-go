package main

import (
	"fmt"
	"time"

	"github.com/Cado-Labs/posthog-go"
)

func TestCapture() {
	// Tests against "Testing" project in app.posthog.com
	client, _ := posthog.NewWithConfig("phc_X8B6bhR1QgQKP1WdpFLN82LxLxgZ7WPXDgJyRyvIpib", posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
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

func TestCaptureWithSendFeatureFlagOption() {
	client, _ := posthog.NewWithConfig("phc_X8B6bhR1QgQKP1WdpFLN82LxLxgZ7WPXDgJyRyvIpib", posthog.Config{
		Interval:       30 * time.Second,
		BatchSize:      100,
		Verbose:        true,
		PersonalApiKey: "a secret key",
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
