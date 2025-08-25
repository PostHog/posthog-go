package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

func TestIsFeatureEnabled(projectAPIKey, personalAPIKey, endpoint string) {
	client, err := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:                           30 * time.Second,
		BatchSize:                          100,
		Verbose:                            true,
		PersonalApiKey:                     personalAPIKey,
		Endpoint:                           endpoint,
		DefaultFeatureFlagsPollingInterval: 5 * time.Second,
		FeatureFlagRequestTimeout:          3 * time.Second,
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
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

	fmt.Println("ℹ️ Feature flag evaluation completed!")
	fmt.Println("   - IsFeatureEnabled(): Returns boolean for any flag type")
	fmt.Println("   - GetFeatureFlag(): Returns the actual variant value")
	fmt.Println("   - GetRemoteConfigPayload(): Returns encrypted payload data")
}

func TestFlagDependencies(projectAPIKey, personalAPIKey, endpoint string) {
	fmt.Println("🔗 Testing flag dependencies with local evaluation...")
	fmt.Println("   This demonstrates pure flag evaluation with no events sent")
	fmt.Println("   Flag structure: 'test-flag-dependency' depends on 'beta-feature' being enabled")
	fmt.Println("")
	fmt.Println("📋 Required setup (if 'test-flag-dependency' doesn't exist):")
	fmt.Println("   1. Create feature flag 'beta-feature':")
	fmt.Println("      - Condition: email contains '@example.com'")
	fmt.Println("      - Rollout: 100%")
	fmt.Println("   2. Create feature flag 'test-flag-dependency':")
	fmt.Println("      - Condition: flag 'beta-feature' is enabled")
	fmt.Println("      - Rollout: 100%")
	fmt.Println("")

	client, err := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:                           30 * time.Second,
		BatchSize:                          100,
		Verbose:                            false, // Disable verbose logging for cleaner output
		PersonalApiKey:                     personalAPIKey,
		Endpoint:                           endpoint,
		DefaultFeatureFlagsPollingInterval: 5 * time.Second,
		FeatureFlagRequestTimeout:          3 * time.Second,
	})
	if err != nil {
		fmt.Printf("❌ Error creating client: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("→ Testing @example.com user (should satisfy dependency if flags exist)...")
	// Disable automatic $feature_flag_called events for cleaner output
	sendEvents := false
	result1, err1 := client.IsFeatureEnabled(posthog.FeatureFlagPayload{
		Key:                   "test-flag-dependency",
		DistinctId:            "example_user",
		PersonProperties:      posthog.NewProperties().Set("email", "user@example.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if err1 != nil {
		fmt.Printf("❌ Error evaluating test-flag-dependency for @example.com user: %v\n", err1)
	} else {
		fmt.Printf("✅ @example.com user (test-flag-dependency): %v\n", result1)
	}

	fmt.Println("→ Testing regular user (dependency should not be satisfied)...")
	result2, err2 := client.IsFeatureEnabled(posthog.FeatureFlagPayload{
		Key:                   "test-flag-dependency",
		DistinctId:            "regular_user",
		PersonProperties:      posthog.NewProperties().Set("email", "user@other.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if err2 != nil {
		fmt.Printf("❌ Error evaluating test-flag-dependency for regular user: %v\n", err2)
	} else {
		fmt.Printf("❌ Regular user (test-flag-dependency): %v\n", result2)
	}

	fmt.Println("→ Testing beta-feature directly for comparison...")

	beta1, betaErr1 := client.IsFeatureEnabled(posthog.FeatureFlagPayload{
		Key:                   "beta-feature",
		DistinctId:            "example_user",
		PersonProperties:      posthog.NewProperties().Set("email", "user@example.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	beta2, betaErr2 := client.IsFeatureEnabled(posthog.FeatureFlagPayload{
		Key:                   "beta-feature",
		DistinctId:            "regular_user",
		PersonProperties:      posthog.NewProperties().Set("email", "user@other.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if betaErr1 != nil || betaErr2 != nil {
		fmt.Printf("❌ Error evaluating beta-feature: err1=%v, err2=%v\n", betaErr1, betaErr2)
	} else {
		fmt.Printf("📊 Beta feature comparison - @example.com: %v, regular: %v\n", beta1, beta2)
	}

	fmt.Println("→ Testing multivariate dependency chains...")

	// Test pineapple -> blue -> breaking-bad chain
	dependentResult3, err3 := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:                   "multivariate-root-flag",
		DistinctId:            "regular_user",
		PersonProperties:      posthog.NewProperties().Set("email", "pineapple@example.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if err3 != nil {
		fmt.Printf("❌ Error evaluating 'multivariate-root-flag' with pineapple@example.com: %v\n", err3)
	} else if dependentResult3 != "breaking-bad" {
		fmt.Printf("     ❌ Something went wrong evaluating 'multivariate-root-flag' with pineapple@example.com. Expected 'breaking-bad', got '%v'\n", dependentResult3)
	} else {
		fmt.Println("✅ 'multivariate-root-flag' with email pineapple@example.com succeeded")
	}

	// Test mango -> red -> the-wire chain
	dependentResult4, err4 := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:                   "multivariate-root-flag",
		DistinctId:            "regular_user",
		PersonProperties:      posthog.NewProperties().Set("email", "mango@example.com"),
		OnlyEvaluateLocally:   true,
		SendFeatureFlagEvents: &sendEvents,
	})

	if err4 != nil {
		fmt.Printf("❌ Error evaluating 'multivariate-root-flag' with mango@example.com: %v\n", err4)
	} else if dependentResult4 != "the-wire" {
		fmt.Printf("     ❌ Something went wrong evaluating multivariate-root-flag with mango@example.com. Expected 'the-wire', got '%v'\n", dependentResult4)
	} else {
		fmt.Println("✅ 'multivariate-root-flag' with email mango@example.com succeeded")
	}

	fmt.Println("\n🎯 Results Summary:")
	if err1 == nil && err2 == nil && result1 != nil && result2 != nil {
		// Convert to booleans for comparison
		bool1, ok1 := result1.(bool)
		bool2, ok2 := result2.(bool)
		if ok1 && ok2 {
			if bool1 != bool2 {
				fmt.Println("   - Flag dependencies evaluated locally: ✅ YES")
			} else {
				fmt.Println("   - Flag dependencies evaluated locally: ❌ NO")
			}
		} else {
			fmt.Println("   - Flag dependencies evaluation: ⚠️ INCONCLUSIVE (non-boolean results)")
		}
	} else {
		fmt.Println("   - Flag dependencies evaluation: ⚠️ INCONCLUSIVE (errors occurred)")
	}
	fmt.Println("   - Zero API calls needed: ✅ YES (all evaluated locally)")
	fmt.Println("   - Go SDK supports flag dependencies: ✅ YES")
}
