package posthog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPollerFailedFlagTelemetryRetainsResponseErrors(t *testing.T) {
	for _, globalError := range []string{"true", "false", "absent"} {
		for _, inconclusive := range []bool{false, true} {
			for _, variant := range []string{"null", `"placeholder"`} {
				name := globalError + "/missing-local/" + variant
				definitions := localDefinitions()
				if inconclusive {
					name = globalError + "/inconclusive-local/" + variant
					definitions = localDefinitions(continuityCheckoutDefinition)
				}
				t.Run(name, func(t *testing.T) {
					globalField := ""
					if globalError != "absent" {
						globalField = `"errorsWhileComputingFlags":` + globalError + `,`
					}
					remote := `{` + globalField + `"flags":{"checkout":{"key":"checkout","enabled":false,"variant":` + variant + `,"failed":true,"metadata":{"payload":"\"remote\""}},"healthy":{"key":"healthy","enabled":false,"failed":false}}}`
					client, capture, _ := newUnresolvedFlagsClient(t, definitions, remote)
					config := FeatureFlagPayload{Key: "checkout", DistinctId: "person"}
					for i := 0; i < 2; i++ {
						result, err := client.GetFeatureFlagResult(config)
						require.Nil(t, result)
						require.ErrorIs(t, err, ErrFlagNotFound)
					}
					expected := FeatureFlagErrorEvaluationFailed
					if globalError == "true" {
						expected = FeatureFlagErrorErrorsWhileComputing + "," + expected
					}
					require.Equal(t, expected, flagCalledError(t, client, capture, "checkout"))
					events := snapshotCapturedEvents(capture)
					require.Equal(t, 1, countFlagCalledEvents(events, "checkout"))
					require.Nil(t, findEvent(events, "$feature_flag_called", "checkout").Properties["$feature_flag_response"])

					config.DistinctId = "suppressed"
					config.SendFeatureFlagEvents = Ptr(false)
					result, err := client.GetFeatureFlagResult(config)
					require.Nil(t, result)
					require.ErrorIs(t, err, ErrFlagNotFound)
					flushEvaluationEvents(t, client)
					require.Equal(t, 1, countFlagCalledEvents(snapshotCapturedEvents(capture), "checkout"))

					healthy, err := client.GetFeatureFlagResult(FeatureFlagPayload{Key: "healthy", DistinctId: "person"})
					require.NoError(t, err)
					require.False(t, healthy.Enabled)
					healthyError := flagCalledError(t, client, capture, "healthy")
					require.NotContains(t, healthyError, FeatureFlagErrorEvaluationFailed)
					if globalError == "true" {
						require.Equal(t, FeatureFlagErrorErrorsWhileComputing, healthyError)
					} else {
						require.Empty(t, healthyError)
					}
				})
			}
		}
	}
}
