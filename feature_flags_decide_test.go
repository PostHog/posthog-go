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
	validateCapturedEvent := func(t *testing.T, client Client) {
		lastEvent := client.GetLastCapturedEvent()

		if lastEvent == nil {
			return
		}
		if lastEvent.Event != "$feature_flag_called" {
			t.Errorf("Expected a $feature_flag_called event, got: %v", lastEvent)
		}

		if lastEvent.Properties["$feature_flag_request_id"] != "42853c54-1431-4861-996e-3a548989fa2c" {
			t.Errorf("Expected $feature_flag_request_id property to be 42853c54-1431-4861-996e-3a548989fa2c, got: %v", lastEvent.Properties["$feature_flag_request_id"])
		}
	}

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

					validateCapturedEvent(t, client)
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

					validateCapturedEvent(t, client)
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

					validateCapturedEvent(t, client)
				})
			}
		})
	}
}

func TestDecideV4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v4.json")))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint: server.URL,
	})
	defer client.Close()

	tests := []struct {
		name            string
		flagKey         string
		expected        interface{}
		expectedVersion int
		expectedReason  string
		expectedId      int
	}{
		{name: "GetFeatureFlag", flagKey: "enabled-flag", expected: true, expectedVersion: 23, expectedReason: "Matched conditions set 3", expectedId: 1},
		{name: "GetFeatureFlag", flagKey: "disabled-flag", expected: false, expectedVersion: 12, expectedReason: "No matching condition set", expectedId: 3},
		{name: "GetFeatureFlag", flagKey: "multi-variate-flag", expected: "hello", expectedVersion: 42, expectedReason: "Matched conditions set 2", expectedId: 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flag, _ := client.GetFeatureFlag(
				FeatureFlagPayload{
					Key:        test.flagKey,
					DistinctId: "some-distinct-id",
				},
			)

			if flag != test.expected {
				t.Errorf("Expected GetFeatureFlag(%s) to return %v but was %v", test.flagKey, test.expected, flag)
			}

			capturedEvent := client.GetLastCapturedEvent()
			if capturedEvent == nil {
				t.Errorf("Expected a $feature_flag_called for %s event, got: %v", test.flagKey, capturedEvent)
			}

			if capturedEvent.Properties["$feature_flag"] != test.flagKey {
				t.Errorf("Expected $feature_flag property to be %v, got: %v", test.flagKey, capturedEvent.Properties["$feature_flag"])
			}

			if capturedEvent.Properties["$feature_flag_request_id"] != "42853c54-1431-4861-996e-3a548989fa2c" {
				t.Errorf("Expected $feature_flag_request_id property to be 42853c54-1431-4861-996e-3a548989fa2c, got: %v", capturedEvent.Properties["$feature_flag_request_id"])
			}

			if capturedEvent.Properties["$feature_flag_response"] != test.expected {
				t.Errorf("Expected $feature_flag_response property for %s to be %v, got: %v", test.flagKey, test.expected, capturedEvent.Properties["$feature_flag_response"])
			}

			if version, ok := capturedEvent.Properties["$feature_flag_version"].(int); !ok || version != test.expectedVersion {
				t.Errorf("Expected $feature_flag_version property for %s to be %v, got: %v", test.flagKey, test.expectedVersion, capturedEvent.Properties["$feature_flag_version"])
			}

			if reason, ok := capturedEvent.Properties["$feature_flag_reason"].(string); !ok || reason != test.expectedReason {
				t.Errorf("Expected $feature_flag_reason property for %s to be %v, got: %v", test.flagKey, test.expectedReason, capturedEvent.Properties["$feature_flag_reason"])
			}

			if id, ok := capturedEvent.Properties["$feature_flag_id"].(int); !ok || int(id) != test.expectedId {
				t.Errorf("Expected $feature_flag_id for %s to be %v, got: %v", test.flagKey, test.expectedId, capturedEvent.Properties["$feature_flag_id"])
			}
		})
	}
}
