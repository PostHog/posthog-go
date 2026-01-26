package posthog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComplexCohortsLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-complex-cohorts-locally.json"))) // Don't return anything for local eval
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	payload := FeatureFlagPayload{
		Key:              "beta-feature",
		DistinctId:       "some-distinct-id",
		PersonProperties: NewProperties().Set("region", "UK"),
	}

	isMatch, err := client.IsFeatureEnabled(payload)
	if err != nil {
		t.Fatal(err)
	}
	if isMatch != false {
		t.Error("Should not match")
	}

	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	isMatch, _ = client.IsFeatureEnabled(payload)
	if isMatch != true {
		t.Error("Should match")
	}

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	isMatch, _ = client.IsFeatureEnabled(payload)
	if isMatch != true {
		t.Error("Should match")
	}
}

func TestComplexCohortsWithNegationLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-complex-cohorts-negation-locally.json"))) // Don't return anything for local eval
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	payload := FeatureFlagPayload{
		Key:              "beta-feature",
		DistinctId:       "some-distinct-id",
		PersonProperties: NewProperties().Set("region", "UK"),
	}

	isMatch, err := client.IsFeatureEnabled(payload)
	if err != nil {
		t.Fatal(err)
	}
	if isMatch != false {
		t.Error("Should not match")
	}

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	isMatch, _ = client.IsFeatureEnabled(payload)
	if isMatch != true {
		t.Error("Should match")
	}

	// # since 'other' is negated, we return False. Since 'nation' is not present, we can't tell whether the flag should be true or false, so go to decide
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	_, err = client.IsFeatureEnabled(payload)
	if err != nil {
		t.Error("Expected to fail")
	}

	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing2")
	isMatch, _ = client.IsFeatureEnabled(payload)
	if isMatch != true {
		t.Error("Should match")
	}
}
