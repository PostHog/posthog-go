package main

import (
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

func TestIsFeatureEnabled() {
	client, _ := posthog.NewWithConfig("phc_36WfBWNJEQcYotMZ7Ui7EWzqKLbIo2LWJFG5fIg1EER", posthog.Config{
		Interval:                           30 * time.Second,
		BatchSize:                          100,
		Verbose:                            true,
		PersonalApiKey:                     "phx_DvugINPCOSM3Ko929TaeywnUlRC5FeF4X7KV60IgyXWGTLw",
		Endpoint:                           "http://localhost:8000",
		DefaultFeatureFlagsPollingInterval: 5 * time.Second,
		FeatureFlagRequestTimeout:          3 * time.Second,
	})
	defer client.Close()

	boolResult, boolErr := client.IsFeatureEnabled(
		posthog.FeatureFlagPayload{
			Key:        "multivariate-test",
			DistinctId: "hello",
		})

	fmt.Println("boolResult:", boolResult)

	if boolErr != nil || boolResult == nil {
		fmt.Println("error:", boolErr)
	}

	// Simple flag
	simpleResult, simpleErr := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "simple-test",
		DistinctId: "hello",
	})

	fmt.Println("simpleResult:", simpleResult)
	if simpleErr != nil || simpleResult == false {
		fmt.Println("error:", simpleErr)
	}

	// Multivariate flag
	variantResult, variantErr := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "multivariate-test",
		DistinctId: "hello",
	})
	fmt.Println("variantResult:", variantResult)
	if variantErr != nil || variantResult != "variant-value" {
		fmt.Println("error:", variantErr)
	}

	// Multivariate + simple flag
	variantResult, variantErr = client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:        "multivariate-simple-test",
		DistinctId: "hello",
	})
	fmt.Println("variantResult:", variantResult)
	if variantErr != nil || variantResult == true {
		fmt.Println("error:", variantErr)
	}

	// Encrypted remote config flag
	payloadResult, payloadErr := client.GetDecryptedFeatureFlagPayload("my_secret_flag_value")
	fmt.Println("payloadResult:", payloadResult)
	if payloadErr != nil {
		fmt.Println("error:", payloadErr)
	}
}
