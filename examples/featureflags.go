package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

func TestIsFeatureEnabled(projectAPIKey, personalAPIKey, endpoint string) {
	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:                           30 * time.Second,
		BatchSize:                          100,
		Verbose:                            true,
		PersonalApiKey:                     personalAPIKey,
		Endpoint:                           endpoint,
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

	// Encrypted remote config flag (string payload)
	stringPayloadResult, stringPayloadErr := client.GetRemoteConfigPayload("my_secret_flag_value")
	fmt.Println("stringPayloadResult:", stringPayloadResult)
	if stringPayloadErr != nil {
		fmt.Println("error:", stringPayloadErr)
	}

	// Encrypted remote config flag (json object payload)
	jsonObjectPayloadResult, _ := client.GetRemoteConfigPayload("my_secret_flag_json_object_value")
	var jsonPayloadMap map[string]interface{}
	json.Unmarshal([]byte(jsonObjectPayloadResult), &jsonPayloadMap)

	// Encrypted remote config flag (json array payload)
	jsonArrayPayloadResult, _ := client.GetRemoteConfigPayload("my_secret_flag_json_array_value")
	var jsonArrayPayload []string
	json.Unmarshal([]byte(jsonArrayPayloadResult), &jsonArrayPayload)
}
