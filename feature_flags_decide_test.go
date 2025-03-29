package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"testing"
)

/*
Tests of the feature flag api against the decide endpoint, no local evaluation.
This is primarily here to ensure we handle the different versions of the decide
endpoint correctly.
*/
func TestDecide(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{name: "v3", fixture: "test-decide-v3.json"},
		{name: "v4", fixture: "test-decide-v4.json"},
	}

	for _, test := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/decide") {
				w.Write([]byte(fixture(test.fixture)))
			}
		}))
		defer server.Close()

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "IsFeatureEnabled", flagKey: "enabled-flag", expected: true},
				{name: "IsFeatureEnabled", flagKey: "disabled-flag", expected: false},
				{name: "IsFeatureEnabled", flagKey: "non-existent-flag", expected: false}, // Note: This differs from posthog-node which returns undefined for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint: server.URL,
					})
					defer client.Close()

					isMatch, _ := client.IsFeatureEnabled(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if isMatch != subTest.expected {
						t.Errorf("Expected IsFeatureEnabled to return %v but was %v", subTest.expected, isMatch)
					}
				})
			}
		})

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "GetFeatureFlag", flagKey: "enabled-flag", expected: true},
				{name: "GetFeatureFlag", flagKey: "disabled-flag", expected: false},
				{name: "GetFeatureFlag", flagKey: "multi-variate-flag", expected: "hello"},
				{name: "GetFeatureFlag", flagKey: "non-existent-flag", expected: false}, // Note: This differs from posthog-node which returns undefined for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint: server.URL,
					})
					defer client.Close()

					flag, _ := client.GetFeatureFlag(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if flag != subTest.expected {
						t.Errorf("Expected GetFeatureFlag to return %v but was %v", subTest.expected, flag)
					}
				})
			}
		})

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "GetFeatureFlagPayload", flagKey: "enabled-flag", expected: "{\"foo\": 1}"},
				{name: "GetFeatureFlagPayload", flagKey: "disabled-flag", expected: ""}, // Incorrectly returns "" for disabled flags
				{name: "GetFeatureFlagPayload", flagKey: "multi-variate-flag", expected: "this is the payload"},
				{name: "GetFeatureFlagPayload", flagKey: "non-existent-flag", expected: ""}, // Incorrectly returns "" for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint: server.URL,
					})
					defer client.Close()

					payload, _ := client.GetFeatureFlagPayload(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if payload != subTest.expected {
						t.Errorf("Expected GetFeatureFlagPayload to return %v but was %v", subTest.expected, payload)
					}
				})
			}
		})
	}
}
