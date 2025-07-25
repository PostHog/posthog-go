package posthog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMatchPropertyValue(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    "Chrome",
		Operator: "exact",
	}

	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if err != nil || !isMatch {
		t.Error("Value is not a match")
	}

}

func TestMatchPropertyInvalidOperator(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    "Chrome",
		Operator: "is_unknown",
	}

	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if isMatch == true {
		t.Error("Should not match")
	}

	var inconclusiveErr *InconclusiveMatchError
	if !errors.As(err, &inconclusiveErr) {
		t.Error("Error type is not a match")
	}

}

func TestMatchPropertySlice(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    []interface{}{"Chrome", "Firefox"},
		Operator: "exact",
	}

	for _, tt := range []struct {
		name       string
		properties Properties
		expected   bool
		err        error
	}{
		{
			name:       "match with Chrome",
			properties: NewProperties().Set("Browser", "Chrome"),
			expected:   true,
		},
		{
			name:       "match with Firefox",
			properties: NewProperties().Set("Browser", "Firefox"),
			expected:   true,
		},
		{
			name:       "no match with Explorer",
			properties: NewProperties().Set("Browser", "Explorer"),
		},
		{
			name:       "no match with unknown key",
			properties: NewProperties().Set("Car", "Chrome"),
			err:        errors.New(""),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isMatch, err := matchProperty(property, tt.properties)
			if tt.err != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.expected, isMatch)
		})
	}
}

func TestMatchPropertySliceExact(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    []interface{}{"Chrome", "Firefox"},
		Operator: "exact",
	}

	isMatch, err := matchProperty(property, NewProperties().Set("Browser", "Chrome"))
	require.NoError(t, err)
	require.True(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Browser", "Firefox"))
	require.NoError(t, err)
	require.True(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Browser", "Explorer"))
	require.NoError(t, err)
	require.False(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Car", "Fiat"))
	require.Error(t, err)
	require.False(t, isMatch)
}

func TestMatchPropertyNumber(t *testing.T) {
	property := FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "gt",
	}

	properties := NewProperties().Set("Number", 7)

	isMatch, err := matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "lt",
	}

	properties = NewProperties().Set("Number", 4)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "gte",
	}

	properties = NewProperties().Set("Number", 5)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "lte",
	}

	properties = NewProperties().Set("Number", 4)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}
}

func TestMatchPropertyRegex(t *testing.T) {

	shouldMatch := []interface{}{"value.com", "value2.com"}

	property := FlagProperty{
		Key:      "key",
		Value:    "\\.com$",
		Operator: "regex",
	}

	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}

	shouldNotMatch := []interface{}{".com343tfvalue5", "Alakazam", 123}

	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}

	// invalid regex
	property = FlagProperty{
		Key:      "key",
		Value:    "?*",
		Operator: "regex",
	}

	shouldNotMatch = []interface{}{"value", "valu2"}
	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}

	// non string value

	property = FlagProperty{
		Key:      "key",
		Value:    4,
		Operator: "regex",
	}

	shouldMatch = []interface{}{"4", 4}
	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}
}

func TestMatchPropertyContains(t *testing.T) {
	shouldMatch := []interface{}{"value", "value2", "value3", "value4", "343tfvalue5"}

	property := FlagProperty{
		Key:      "key",
		Value:    "valUe",
		Operator: "icontains",
	}

	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}

	shouldNotMatch := []interface{}{"Alakazam", 123}

	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}
}

func TestFlagPersonProperty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:              "simple-flag",
			DistinctId:       "some-distinct-id",
			PersonProperties: NewProperties().Set("region", "USA"),
		},
	)

	if isMatch != true {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:              "simple-flag",
			DistinctId:       "some-distinct-id",
			PersonProperties: NewProperties().Set("region", "Canada"),
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}
}

func TestFlagGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			var reqBody FlagsRequestData
			err := decoder.Decode(&reqBody)
			if err != nil {
				t.Error(err)
			}

			groupsEquality := reflect.DeepEqual(reqBody.Groups, Groups{"company": "abc"})
			if !groupsEquality {
				t.Errorf("Expected groups to be map[company:abc], got %s", reqBody.Groups)
			}

			distinctIdEquality := reflect.DeepEqual(reqBody.DistinctId, "-")
			if !distinctIdEquality {
				t.Errorf("Expected distinctId to be -, got %s", reqBody.DistinctId)
			}

			apiKeyEquality := reflect.DeepEqual(reqBody.ApiKey, "Csyjlnlun3OzyNJAafdlv")
			if !apiKeyEquality {
				t.Errorf("Expected apiKey to be Csyjlnlun3OzyNJAafdlv, got %s", reqBody.ApiKey)
			}

			personPropertiesEquality := reflect.DeepEqual(reqBody.PersonProperties, Properties{"region": "Canada"})
			if !personPropertiesEquality {
				t.Errorf("Expected personProperties to be map[region:Canada], got %s", reqBody.PersonProperties)
			}

			groupPropertiesEquality := reflect.DeepEqual(reqBody.GroupProperties, map[string]Properties{"company": {"name": "Project Name 1"}})
			if !groupPropertiesEquality {
				t.Errorf("Expected groupProperties to be map[company:map[name:Project Name 1]], got %s", reqBody.GroupProperties)
			}
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-flag-group-properties.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// Ignore batch requests
		} else {
			t.Error("Unknown request made by library")
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "unknown-flag",
			DistinctId:          "-",
			Groups:              Groups{"company": "abc"},
			PersonProperties:    NewProperties().Set("region", "Canada"),
			GroupProperties:     map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
			OnlyEvaluateLocally: false,
		},
	)

	if isMatch != false {
		t.Error("Unknown flag shouldn't match known flags")
	}
}

func TestFlagGroupProperty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-flag-group-properties.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:             "group-flag",
			DistinctId:      "some-distinct-id",
			GroupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:             "group-flag",
			DistinctId:      "some-distinct-id",
			GroupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 2")},
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:             "group-flag",
			DistinctId:      "some-distinct-id",
			Groups:          Groups{"company": "amazon_without_rollout"},
			GroupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
		},
	)

	if isMatch != true {
		t.Error("Should match")
	}
}

func TestComplexDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-complex-definition.json"))) // Don't return anything for local eval
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	require.NoError(t, err)
	defer client.Close()

	isMatch, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:              "complex-flag",
			DistinctId:       "some-distinct-id",
			PersonProperties: NewProperties().Set("region", "USA").Set("name", "Aloha"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, true, isMatch)

	isMatch, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:              "complex-flag",
			DistinctId:       "some-distinct-id_within_rollout_3",
			PersonProperties: NewProperties().Set("region", "USA").Set("email", "a@b.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, true, isMatch)
}

func TestFallbackToDecide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte("{}")) // Don't return anything for local eval
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "simple-flag",
			DistinctId: "some-distinct-id",
		},
	)

	if isMatch != true {
		t.Error("Should match")
	}
}

func TestFeatureFlagsDontFallbackToDecideWhenOnlyLocalEvaluationIsTrue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte("test-decide-v3.json"))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-feature-flags-dont-fallback-to-decide-when-only-local-evaluation-is-true.json")))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	matchedPayload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:                 "beta-feature",
			DistinctId:          "some-distinct-id",
			OnlyEvaluateLocally: true,
		},
	)

	if matchedPayload != "" {
		t.Error("Should not match")
	}

	matchedVariant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:                 "beta-feature",
			DistinctId:          "some-distinct-id",
			OnlyEvaluateLocally: true,
		},
	)

	if matchedVariant != nil {
		t.Error("Should not match")
	}

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "beta-feature",
			DistinctId:          "some-distinct-id",
			OnlyEvaluateLocally: true,
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	matchedVariant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:                 "beta-feature2",
			DistinctId:          "some-distinct-id",
			OnlyEvaluateLocally: true,
		},
	)

	if matchedVariant != nil {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "beta-feature2",
			DistinctId:          "some-distinct-id",
			OnlyEvaluateLocally: true,
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}
}

func TestFeatureFlagDefaultsDontHinderEvaluation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-false.json")))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "false-flag",
			DistinctId: "some-distinct-id",
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "false-flag",
			DistinctId: "some-distinct-id",
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "false-flag-2",
			DistinctId: "some-distinct-id",
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "false-flag-2",
			DistinctId: "some-distinct-id",
		},
	)

	if isMatch == true {
		t.Error("Should not match")
	}

}

func TestFeatureFlagNullComeIntoPlayOnlyWhenDecideErrorsOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{ads}"))
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	matchedPayload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if matchedPayload != "" {
		t.Error("Should not match")
	}

	isMatch, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if isMatch != nil {
		t.Error("Should be nil")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if isMatch != nil {
		t.Error("Should be nil")
	}
}

func TestExperienceContinuityOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-simple-flag.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "distinct_id",
		},
	)

	if featureVariant != "decide-fallback-value" {
		t.Error("Should be decide-fallback-value")
	}

	payload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "distinct_id",
		},
	)

	if payload != "{\"foo\": \"bar\"}" {
		t.Error(`Should be "{"foo": "bar"}"`)
	}
}

func TestGetAllFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-multiple-flags.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariants, _ := client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "distinct-id",
	})

	if featureVariants["beta-feature"] != "decide-fallback-value" || featureVariants["beta-feature2"] != "variant-2" {
		t.Error("Should match decide values")
	}
}

func TestGetAllFlagsEmptyLocal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte("{}"))
		}
	}))

	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	require.NoError(t, err)
	defer client.Close()

	featureVariants, err := client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "distinct-id",
	})
	require.NoError(t, err)

	if featureVariants["beta-feature"] != "decide-fallback-value" || featureVariants["beta-feature2"] != "variant-2" {
		t.Error("Should match decide values")
	}
}

func TestGetAllFlagsNoDecide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-multiple-flags-valid.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariants, _ := client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "distinct-id",
	})

	if featureVariants["beta-feature"] != true || featureVariants["disabled-feature"] != false {
		t.Error("Should match")
	}
}

func TestGetAllFlagsOnlyLocalEvaluationSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-get-all-flags-with-fallback-but-only-local-evaluation-set.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariants, _ := client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId:          "distinct-id",
		OnlyEvaluateLocally: true,
	})

	if featureVariants["beta-feature"] != true || featureVariants["disabled-feature"] != false || featureVariants["beta-feature2"] != nil {
		t.Error("Should match")
	}
}

func TestComputeInactiveFlagsLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-compute-inactive-flags-locally.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariants, _ := client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "distinct-id",
	})

	if featureVariants["beta-feature"] != true || featureVariants["disabled-feature"] != false {
		t.Error("Should match")
	}

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-compute-inactive-flags-locally-2.json")))
		}
	}))

	defer server.Close()

	client, _ = NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	featureVariants, _ = client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "distinct-id",
	})

	if featureVariants["beta-feature"] != false || featureVariants["disabled-feature"] != true {
		t.Error("Should match")
	}
}

func TestFeatureFlagWithDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-flag-with-dependencies.json")))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Test that flag evaluation doesn't fail when encountering a flag dependency
	isMatch, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-with-dependencies",
			DistinctId:          "test-user",
			PersonProperties:    NewProperties().Set("email", "test@example.com"),
			OnlyEvaluateLocally: true,
		},
	)

	// Should not error out
	if err != nil {
		t.Errorf("Should not return error when encountering flag dependencies: %v", err)
	}

	// The flag should evaluate based on other conditions (email contains @example.com)
	// Since flag dependencies aren't implemented, it should skip the flag condition
	// and evaluate based on the email condition only
	if isMatch != true {
		t.Error("Should match based on email condition")
	}

	// Test with email that doesn't match
	isMatch, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-with-dependencies",
			DistinctId:          "test-user-2",
			PersonProperties:    NewProperties().Set("email", "test@other.com"),
			OnlyEvaluateLocally: true,
		},
	)

	if err != nil {
		t.Errorf("Should not return error: %v", err)
	}

	if isMatch == true {
		t.Error("Should not match when email doesn't contain @example.com")
	}
}

func TestFeatureEnabledSimpleIsTrueWhenRolloutUndefined(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-simple-flag-without-rollout.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "simple-flag",
			DistinctId: "distinct-id",
		},
	)
	if isMatch != true {
		t.Error("Should be enabled")
	}
}

func TestGetFeatureFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if variant != "variant-1" {
		t.Error("Should match")
	}
}

func TestGetFeatureFlagPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if variant != "this is a string" {
		t.Error("Should match")
	}
}

func TestGetRemoteConfigPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("test-remote-config.json")))
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	payload, _ := client.GetRemoteConfigPayload("flag_key")

	var payloadMap map[string]interface{}
	err := json.Unmarshal([]byte(payload), &payloadMap)
	if err != nil {
		t.Error("Failed to decode payload")
	}

	if payloadMap["foo"] != "bar" || payloadMap["baz"] != float64(42) {
		t.Error("Should match")
	}
}

func TestFlagWithVariantOverrides(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-variant-override.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "first-variant" {
		t.Error("Should match", variant, "first-variant")
	}

	payload, _ = client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if payload != "{\"test\": 1}" {
		t.Error("Should match", payload, "{\"test\": 1}")
	}
}

func TestFlagWithClashingVariantOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-variant-override-clashing.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "example_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ = client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "example_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}
}

func TestFlagWithInvalidVariantOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-variant-override-invalid.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if variant != "third-variant" {
		t.Error("Should match", variant, "third-variant")
	}

	payload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if payload != "{\"test\": 3}" {
		t.Error("Should match", payload, "{\"test\": 3}")
	}

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ = client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}
}

func TestFlagWithMultipleVariantOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-variant-override-multiple.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ := client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "test_id",
			PersonProperties: NewProperties().Set("email", "test@posthog.com"),
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "third-variant" {
		t.Error("Should match", variant, "third-variant")
	}

	payload, _ = client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if payload != "{\"test\": 3}" {
		t.Error("Should match", payload, "{\"test\": 3}")
	}

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "another_id",
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "second-variant")
	}

	payload, _ = client.GetFeatureFlagPayload(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "another_id",
		},
	)

	if payload != "{\"test\": 2}" {
		t.Error("Should match", payload, "{\"test\": 2}")
	}
}

func TestCaptureIsCalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
		}
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	variant, _ := client.GetFeatureFlag(

		FeatureFlagPayload{
			Key:        "test-get-feature",
			DistinctId: "distinct_id",
		},
	)

	if variant != "variant-1" {
		t.Error("Should match")
	}

}

func TestSimpleFlagConsistency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-simple-flag.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	results := []bool{
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		false,
		true,
		true,
		false,
		true,
		true,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		false,
		false,
		false,
		false,
		true,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
		false,
		false,
		true,
		true,
		true,
		false,
		true,
		false,
		false,
		true,
		true,
		false,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		false,
		true,
		true,
		true,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		true,
		false,
		false,
		true,
		false,
		false,
		true,
		false,
		true,
		true,
	}

	for i := 0; i < 1000; i++ {
		isMatch, _ := client.IsFeatureEnabled(
			FeatureFlagPayload{
				Key:        "simple-flag",
				DistinctId: fmt.Sprintf("%s%d", "distinct_id_", i),
			},
		)
		if results[i] != isMatch {
			t.Error("Match result is not consistent")
		}
	}
}

func TestMultivariateFlagConsistency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-multivariate-flag.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	results := []interface{}{
		"second-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"third-variant",
		false,
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		"fourth-variant",
		"first-variant",
		false,
		"third-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		false,
		"third-variant",
		"second-variant",
		"first-variant",
		false,
		"third-variant",
		false,
		false,
		"first-variant",
		"second-variant",
		false,
		"first-variant",
		"first-variant",
		"second-variant",
		false,
		"first-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"second-variant",
		"second-variant",
		"third-variant",
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		"second-variant",
		"fourth-variant",
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"second-variant",
		false,
		"third-variant",
		false,
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"fifth-variant",
		false,
		"second-variant",
		"first-variant",
		"second-variant",
		false,
		"third-variant",
		"third-variant",
		false,
		false,
		false,
		false,
		"third-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		"third-variant",
		false,
		"third-variant",
		"second-variant",
		"third-variant",
		false,
		false,
		"second-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		false,
		"second-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"second-variant",
		"second-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		"third-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		"fifth-variant",
		"second-variant",
		false,
		"second-variant",
		false,
		"first-variant",
		"third-variant",
		"first-variant",
		"fifth-variant",
		"third-variant",
		false,
		false,
		"fourth-variant",
		false,
		false,
		false,
		false,
		"third-variant",
		false,
		false,
		"third-variant",
		false,
		"first-variant",
		"second-variant",
		"second-variant",
		"second-variant",
		false,
		"first-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		false,
		false,
		false,
		"second-variant",
		false,
		false,
		"first-variant",
		false,
		"first-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		"first-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		"third-variant",
		"third-variant",
		false,
		"second-variant",
		"first-variant",
		false,
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		false,
		false,
		"first-variant",
		"fifth-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"second-variant",
		false,
		"second-variant",
		"third-variant",
		"third-variant",
		false,
		"first-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		"third-variant",
		"first-variant",
		false,
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"second-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		false,
		"first-variant",
		false,
		"third-variant",
		false,
		"third-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		"third-variant",
		"first-variant",
		"second-variant",
		"fifth-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		"third-variant",
		false,
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		"third-variant",
		false,
		false,
		"third-variant",
		false,
		false,
		"first-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		"fourth-variant",
		"fourth-variant",
		"third-variant",
		"second-variant",
		"first-variant",
		"third-variant",
		"fifth-variant",
		false,
		"first-variant",
		"fifth-variant",
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		"fifth-variant",
		"second-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		false,
		false,
		"third-variant",
		false,
		"second-variant",
		"fifth-variant",
		false,
		"third-variant",
		"first-variant",
		false,
		false,
		"fourth-variant",
		false,
		false,
		"second-variant",
		false,
		false,
		"first-variant",
		"fourth-variant",
		"first-variant",
		"second-variant",
		false,
		false,
		false,
		"first-variant",
		"third-variant",
		"third-variant",
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		"third-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		"second-variant",
		"second-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"fifth-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		"third-variant",
		"first-variant",
		"fourth-variant",
		"first-variant",
		"third-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		false,
		"fourth-variant",
		"fifth-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		"second-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		"third-variant",
		"third-variant",
		"first-variant",
		false,
		false,
		"second-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		"third-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"third-variant",
		"third-variant",
		false,
		false,
		false,
		false,
		"third-variant",
		"fourth-variant",
		"fourth-variant",
		"first-variant",
		"second-variant",
		false,
		"first-variant",
		false,
		"second-variant",
		"first-variant",
		"third-variant",
		false,
		"third-variant",
		false,
		"first-variant",
		"first-variant",
		"third-variant",
		false,
		false,
		false,
		"fourth-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		"fourth-variant",
		false,
		"first-variant",
		"third-variant",
		"first-variant",
		false,
		false,
		"third-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		"third-variant",
		"second-variant",
		"fourth-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		"second-variant",
		"first-variant",
		"second-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		"second-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"third-variant",
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		"fifth-variant",
		"fourth-variant",
		"first-variant",
		"second-variant",
		false,
		"fourth-variant",
		false,
		false,
		false,
		"fourth-variant",
		false,
		false,
		"third-variant",
		false,
		false,
		false,
		"first-variant",
		"third-variant",
		"third-variant",
		"second-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		"first-variant",
		false,
		"second-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		"second-variant",
		false,
		false,
		"fifth-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"second-variant",
		"third-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		"third-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		"fourth-variant",
		"first-variant",
		false,
		false,
		false,
		"third-variant",
		false,
		false,
		"second-variant",
		"first-variant",
		false,
		false,
		"second-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		"second-variant",
		"third-variant",
		"second-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		false,
		false,
		"first-variant",
		false,
		"third-variant",
		false,
		"first-variant",
		false,
		false,
		"second-variant",
		"third-variant",
		"second-variant",
		"fourth-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		false,
		false,
		"second-variant",
		false,
		"first-variant",
		false,
		"third-variant",
		false,
		false,
		"first-variant",
		"third-variant",
		false,
		"third-variant",
		false,
		false,
		"second-variant",
		false,
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"second-variant",
		false,
		false,
		"first-variant",
		"third-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"second-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"fifth-variant",
		false,
		false,
		false,
		"first-variant",
		false,
		"third-variant",
		false,
		false,
		"second-variant",
		false,
		false,
		false,
		false,
		false,
		"fourth-variant",
		"second-variant",
		"first-variant",
		"second-variant",
		false,
		"second-variant",
		false,
		"second-variant",
		false,
		"first-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		"second-variant",
		false,
		"first-variant",
		false,
		"fifth-variant",
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		false,
		"first-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		"fifth-variant",
		false,
		false,
		"third-variant",
		false,
		"third-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		"third-variant",
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		"second-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		"fifth-variant",
		"first-variant",
		false,
		false,
		"fourth-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		"fourth-variant",
		"first-variant",
		false,
		"second-variant",
		"third-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		"third-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		"second-variant",
		false,
		"third-variant",
		"first-variant",
		"second-variant",
		"fifth-variant",
		"first-variant",
		"first-variant",
		false,
		"first-variant",
		"fifth-variant",
		false,
		false,
		false,
		"third-variant",
		"first-variant",
		"first-variant",
		"second-variant",
		"fourth-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		false,
		"first-variant",
		false,
		"third-variant",
		"third-variant",
		"first-variant",
		"first-variant",
		false,
		"second-variant",
		false,
		"second-variant",
		"first-variant",
		false,
		false,
		false,
		"second-variant",
		false,
		"third-variant",
		false,
		"first-variant",
		"fifth-variant",
		"first-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"fourth-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"fifth-variant",
		false,
		false,
		false,
		"second-variant",
		false,
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		"second-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		"third-variant",
		"first-variant",
		false,
		"second-variant",
		false,
		false,
		"third-variant",
		"second-variant",
		"third-variant",
		false,
		"first-variant",
		"third-variant",
		"second-variant",
		"first-variant",
		"third-variant",
		false,
		false,
		"first-variant",
		"first-variant",
		false,
		false,
		false,
		"first-variant",
		"third-variant",
		"second-variant",
		"first-variant",
		"first-variant",
		"first-variant",
		false,
		"third-variant",
		"second-variant",
		"third-variant",
		false,
		false,
		"third-variant",
		"first-variant",
		false,
		"first-variant",
	}

	for i := 0; i < 1000; i++ {

		variant, _ := client.GetFeatureFlag(
			FeatureFlagPayload{
				Key:        "multivariate-flag",
				DistinctId: fmt.Sprintf("%s%d", "distinct_id_", i),
			},
		)
		if results[i] != variant {
			t.Error("Match result is not consistent")
		}
	}
}

func TestMultivariateFlagConsistencyPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-multivariate-flag.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	results := []string{
		"{\"test\": 2}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 4}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 5}",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 4}",
		"",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 5}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 4}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 5}",
		"",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 5}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 2}",
		"{\"test\": 5}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 4}",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 4}",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 4}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 4}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 4}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 4}",
		"",
		"",
		"",
		"{\"test\": 4}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 5}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 5}",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 5}",
		"",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 4}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 5}",
		"",
		"",
		"",
		"{\"test\": 2}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"{\"test\": 2}",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"",
		"",
		"{\"test\": 1}",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"{\"test\": 1}",
		"",
		"{\"test\": 3}",
		"{\"test\": 2}",
		"{\"test\": 3}",
		"",
		"",
		"{\"test\": 3}",
		"{\"test\": 1}",
		"",
		"{\"test\": 1}",
	}

	for i := 0; i < 1000; i++ {
		variant, _ := client.GetFeatureFlagPayload(
			FeatureFlagPayload{
				Key:        "multivariate-flag",
				DistinctId: fmt.Sprintf("%s%d", "distinct_id_", i),
			},
		)
		if results[i] != variant {
			t.Errorf("Match result is not consistent, expected %s, got %s", results[i], variant)
		}
	}
}

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

func TestFlagsFetchFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.WriteHeader(http.StatusInternalServerError)
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// ignore batch requests
		} else {
			t.Errorf("Unknown request made by library: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey:            "some very secret key",
		Endpoint:                  server.URL,
		FeatureFlagRequestTimeout: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer client.Close()
	_, err = client.GetFeatureFlag(FeatureFlagPayload{
		Key:                 "enabled-flag",
		DistinctId:          "123",
		OnlyEvaluateLocally: true,
	})
	require.EqualError(t, err, "flags were not successfully fetched yet")
}

func TestFlagWithTimeoutExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			time.Sleep(1 * time.Second)
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-flag-group-properties.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// Ignore batch requests
		} else {
			t.Error("Unknown request made by library")
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey:            "some very secret key",
		Endpoint:                  server.URL,
		FeatureFlagRequestTimeout: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer client.Close()

	isMatch, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "enabled-flag",
			DistinctId: "-",
		},
	)

	require.Error(t, err)
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Error("Expected context deadline exceeded error")
	}
	require.Nil(t, isMatch)

	// get all flags with no local evaluation possible
	variants, err := client.GetAllFlags(
		FeatureFlagPayloadNoKey{
			DistinctId: "-",
			Groups:     Groups{"company": "posthog1"},
		},
	)

	require.Error(t, err)
	require.ErrorContains(t, err, "context deadline exceeded")

	require.Empty(t, variants)

	// get all flags with partial local evaluation possible
	variants, err = client.GetAllFlags(
		FeatureFlagPayloadNoKey{
			DistinctId:       "-",
			Groups:           Groups{"company": "posthog1"},
			PersonProperties: NewProperties().Set("region", "USA"),
		},
	)

	require.Error(t, err)
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Error("Expected context deadline exceeded error")
	}
	require.EqualValues(t, map[string]interface{}{"simple-flag": true}, variants)

	// get all flags with full local evaluation possible
	variants, err = client.GetAllFlags(
		FeatureFlagPayloadNoKey{
			DistinctId:       "-",
			Groups:           Groups{"company": "posthog1"},
			PersonProperties: NewProperties().Set("region", "USA"),
			GroupProperties:  map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
		},
	)

	require.NoError(t, err)
	fmt.Println(variants)
	require.EqualValues(t, map[string]interface{}{"simple-flag": true, "group-flag": true}, variants)
}

func TestFlagDefinitionsWithTimeoutExceeded(t *testing.T) {
	// create buffer to write logs to
	var buf bytes.Buffer

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-decide-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			time.Sleep(11 * time.Second)
			w.Write([]byte(fixture("feature_flag/test-flag-group-properties.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// Ignore batch requests
		} else {
			t.Error("Unknown request made by library")
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey:            "some very secret key",
		Endpoint:                  server.URL,
		FeatureFlagRequestTimeout: 10 * time.Millisecond,
		Logger:                    StdLogger(log.New(&buf, "posthog-test", log.LstdFlags), false),
	})
	defer client.Close()

	_, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "enabled-flag",
			DistinctId: "-",
		},
	)
	if err != nil {
		t.Error("Unexpected error")
	}

	output := buf.String()
	if !strings.Contains(output, "Unable to fetch feature flags") {
		t.Error("Expected error fetching flags")
	}

	if !strings.Contains(output, "context deadline exceeded") {
		t.Error("Expected timeout error fetching flags")
	}
}

func TestFetchFlagsFails(t *testing.T) {
	// This test verifies that even in presence of HTTP errors flags continue to be fetched.
	var called uint32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadUint32(&called) == 0 {
			// Load initial flags successfully
			w.Write([]byte(fixture("feature_flag/test-simple-flag.json")))
		} else {
			// Fail all next requests
			w.WriteHeader(http.StatusInternalServerError)
		}
		atomic.AddUint32(&called, 1)

	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	_, err := client.GetFeatureFlags()
	if err != nil {
		t.Error("Should not fail", err)
	}
	client.ReloadFeatureFlags()
	client.ReloadFeatureFlags()

	_, err = client.GetAllFlags(FeatureFlagPayloadNoKey{
		DistinctId: "my-id",
	})
	if err != nil {
		t.Error("Should not fail", err)
	}

	// Wait for the last request to complete
	<-time.After(50 * time.Millisecond)

	const expectedCalls = 3
	actualCalls := atomic.LoadUint32(&called)
	if actualCalls != expectedCalls {
		t.Error("Expected to be called", expectedCalls, "times but got", actualCalls)
	}
}

// The reference implementation of overrides may be found in Rust feature-flag implementation:
//
// Check link: https://github.com/PostHog/posthog/blob/0bb3ed063c37f5be280e4283a0d2a6a6683a9534/rust/feature-flags/src/api/request_handler.rs#L1412
func TestFeatureFlagWithOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-group-props.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// ignore
		} else {
			t.Errorf("Unknown request made by library: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	cases := []struct {
		name string
		req  FeatureFlagPayload
		want bool
		err  error
	}{
		{
			"matches group_key",
			FeatureFlagPayload{
				Key:        "flag-with-groups",
				DistinctId: "test-id",
				Groups:     Groups{"company": "1"},
			},
			true,
			nil,
		},
		{
			"distinct id does not match",
			FeatureFlagPayload{
				Key:        "flag-with-groups",
				DistinctId: "test-id",
				Groups:     Groups{"company": "2"},
			},
			false,
			nil,
		},
		{
			"matching group_key property is not overridden",
			FeatureFlagPayload{
				Key:        "flag-with-groups",
				DistinctId: "test-id",
				Groups:     Groups{"company": "2", "project": "7"},
				GroupProperties: map[string]Properties{"company": {
					"$group_key": "1",
				}},
			},
			true,
			nil,
		},
		{
			"no match group_key property is not overridden",
			FeatureFlagPayload{
				Key:        "flag-with-groups",
				DistinctId: "test-id",
				Groups:     Groups{"company": "1", "project": "7"},
				GroupProperties: map[string]Properties{"company": {
					"$group_key": "2",
				}},
			},
			false,
			nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(tt *testing.T) {
			enabled, err := client.GetFeatureFlag(c.req)
			require.Equal(tt, c.err, err)
			require.Equal(tt, c.want, enabled)
		})
	}
}

func TestFeatureFlagDistinctIDOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-distinct-id-local.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// ignore
		} else {
			t.Errorf("Unknown request made by library: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	cases := []struct {
		name string
		req  FeatureFlagPayload
		want bool
		err  error
	}{
		{
			"matches distinct id",
			FeatureFlagPayload{
				Key:        "test-boolean-flag-with-rollout-conditions",
				DistinctId: "1",
			},
			true,
			nil,
		},
		{
			"distinct id does not match",
			FeatureFlagPayload{
				Key:        "test-boolean-flag-with-rollout-conditions",
				DistinctId: "2",
			},
			false,
			nil,
		},
		{
			"distinct id property is not override",
			FeatureFlagPayload{
				Key:              "test-boolean-flag-with-rollout-conditions",
				DistinctId:       "2",
				PersonProperties: NewProperties().Set("distinct_id", "1"),
			},
			true,
			nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(tt *testing.T) {
			enabled, err := client.GetFeatureFlag(c.req)
			require.Equal(tt, c.err, err)
			require.Equal(tt, c.want, enabled)
		})
	}
}

func TestFeatureFlagWithFalseVariant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-false-variant.json")))
		} else if strings.HasPrefix(r.URL.Path, "/batch/") {
			// ignore
		} else {
			t.Errorf("Unknown request made by library: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Test GetFeatureFlag - should return the variant name "false"
	variant, err := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "test-id",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "false", variant)

	// Test IsFeatureEnabled - should return the variant name "false"
	// This will fail with the current implementation because it incorrectly converts "false" to false
	isEnabled, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "test-id",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "false", isEnabled)
}
