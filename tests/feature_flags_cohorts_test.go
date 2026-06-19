package posthog

import "testing"

func TestComplexCohortsLocally(t *testing.T) {
	client := newCohortTestClient(t, "feature_flag/test-complex-cohorts-locally.json")
	payload := cohortPayload(NewProperties().Set("region", "UK"))

	assertFeatureEnabled(t, client, payload, false)
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	assertFeatureEnabled(t, client, payload, true)

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	assertFeatureEnabled(t, client, payload, true)
}

func TestComplexCohortsWithNegationLocally(t *testing.T) {
	client := newCohortTestClient(t, "feature_flag/test-complex-cohorts-negation-locally.json")
	payload := cohortPayload(NewProperties().Set("region", "UK"))

	assertFeatureEnabled(t, client, payload, false)

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	assertFeatureEnabled(t, client, payload, true)

	// # since 'other' is negated, we return False. Since 'nation' is not present, we can't tell whether the flag should be true or false, so go to decide
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	if _, err := client.IsFeatureEnabled(payload); err != nil {
		t.Error("Expected to fail")
	}

	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing2")
	assertFeatureEnabled(t, client, payload, true)
}

func newCohortTestClient(t *testing.T, fixtureName string) Client {
	return newFeatureFlagsFixtureClient(t, fixtureName)
}

func cohortPayload(properties Properties) FeatureFlagPayload {
	return FeatureFlagPayload{Key: "beta-feature", DistinctId: "some-distinct-id", PersonProperties: properties}
}

func assertFeatureEnabled(t *testing.T, client Client, payload FeatureFlagPayload, want bool) {
	t.Helper()
	isMatch, err := client.IsFeatureEnabled(payload)
	if err != nil {
		t.Fatal(err)
	}
	if isMatch != want {
		t.Errorf("IsFeatureEnabled = %v, want %v", isMatch, want)
	}
}
