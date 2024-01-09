package posthog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"

	"testing"
)

func TestMatchPropertyValue(t *testing.T) {
	property := Property{
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
	property := Property{
		Key:      "Browser",
		Value:    "Chrome",
		Operator: "is_unknown",
	}

	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if isMatch == true {
		t.Error("Should not match")
	}

	if _, ok := err.(*InconclusiveMatchError); !ok {
		t.Error("Error type is not a match")
	}

}
func TestMatchPropertySlice(t *testing.T) {

	property := Property{
		Key:      "Browser",
		Value:    []interface{}{"Chrome"},
		Operator: "exact",
	}
	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if err != nil || !isMatch {
		t.Error("Value is not a match")
	}

}

func TestMatchPropertyNumber(t *testing.T) {
	property := Property{
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

	property = Property{
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

	property = Property{
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

	property = Property{
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

	property := Property{
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
	property = Property{
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

	property = Property{
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

	property := Property{
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			var reqBody DecideRequestData
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

			groupPropertiesEquality := reflect.DeepEqual(reqBody.GroupProperties, map[string]Properties{"company": Properties{"name": "Project Name 1"}})
			if !groupPropertiesEquality {
				t.Errorf("Expected groupProperties to be map[company:map[name:Project Name 1]], got %s", reqBody.GroupProperties)
			}
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte(fixture("feature_flag/test-complex-definition.json"))) // Don't return anything for local eval
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
			Key:              "complex-flag",
			DistinctId:       "some-distinct-id",
			PersonProperties: NewProperties().Set("region", "USA").Set("name", "Aloha"),
		},
	)

	if isMatch != true {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:              "complex-flag",
			DistinctId:       "some-distinct-id_within_rollou",
			PersonProperties: NewProperties().Set("region", "USA").Set("email", "a@b.com"),
		},
	)

	if isMatch != true {
		t.Error("Should match")
	}

}

func TestFallbackToDecide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte("test-decide-v2.json"))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
}

func TestGetAllFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
		} else if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.Write([]byte("{}"))
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

func TestGetAllFlagsNoDecide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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

func TestFlagWithVariantOverrides(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "first-variant" {
		t.Error("Should match", variant, "first-variant")
	}
}

func TestFlagWithClashingVariantOverrides(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
}

func TestFlagWithInvalidVariantOverrides(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "second-variant" {
		t.Error("Should match", variant, "third-variant")
	}
}

func TestFlagWithMultipleVariantOverrides(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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

	variant, _ = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "beta-feature",
			DistinctId: "example_id",
		},
	)

	if variant != "third-variant" {
		t.Error("Should match", variant, "third-variant")
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
}

func TestCaptureIsCalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/decide") {
			w.Write([]byte(fixture("test-decide-v2.json")))
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
