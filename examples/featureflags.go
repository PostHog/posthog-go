package main

import (
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

func TestIsFeatureEnabled() {
	client, _ := posthog.NewWithConfig("phc_X8B6bhR1QgQKP1WdpFLN82LxLxgZ7WPXDgJyRyvIpib", posthog.Config{
		Interval:       30 * time.Second,
		BatchSize:      100,
		Verbose:        true,
		PersonalApiKey: "phx_vXZ7AOnFjDrCxfWLyo9V6P0SWLLfXT2d5euy3U0nRGk",
	})
	defer client.Close()

	boolResult, boolErr := client.IsFeatureEnabled(
		posthog.FeatureFlagPayload{
			Key:        "multivariate-test",
			DistinctId: "hello",
		})

	if boolErr != nil || !boolResult {
		fmt.Println("error:", boolErr)
		return
	}

	// Simple flag
	simpleResult, simpleErr := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "simple-test",
		DistinctId: "hello",
	})
	if simpleErr != nil || simpleResult == false {
		fmt.Println("error:", simpleErr)
		return
	}

	// Multivariate flag
	variantResult, variantErr := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "multivariate-test",
		DistinctId: "hello",
	})
	if variantErr != nil || variantResult != "variant-value" {
		fmt.Println("error:", variantErr)
		return
	}

	// Multivariate + simple flag
	variantResult, variantErr = client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "multivariate-simple-test",
		DistinctId: "hello",
	})
	if variantErr != nil || variantResult == true {
		fmt.Println("error:", variantErr)
		return
	}
}
