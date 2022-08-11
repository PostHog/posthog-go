package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

	isMatch, _ := client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:              "simple-flag",
			distinctId:       "some-distinct-id",
			personProperties: NewProperties().Set("region", "USA"),
		},
	))

	if !isMatch {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:              "simple-flag",
			distinctId:       "some-distinct-id",
			personProperties: NewProperties().Set("region", "Canada"),
		},
	))

	if isMatch {
		t.Error("Should not match")
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

	isMatch, _ := client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:             "group-flag",
			distinctId:      "some-distinct-id",
			groupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:             "group-flag",
			distinctId:      "some-distinct-id",
			groupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 2")},
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:             "group-flag",
			distinctId:      "some-distinct-id",
			groups:          Groups{"company": "amazon_without_rollout"},
			groupProperties: map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")},
		},
	))

	if !isMatch {
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
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:              "complex-flag",
				distinctId:       "some-distinct-id",
				personProperties: NewProperties().Set("region", "USA").Set("name", "Aloha"),
			},
		),
	)

	if !isMatch {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled(
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:              "complex-flag",
				distinctId:       "some-distinct-id_within_rollou",
				personProperties: NewProperties().Set("region", "USA").Set("email", "a@b.com"),
			},
		),
	)

	if !isMatch {
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
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:        "simple-flag",
				distinctId: "some-distinct-id",
			},
		),
	)

	if !isMatch {
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

	matchedVariant, _ := client.GetFeatureFlag(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:                 "beta-feature",
			distinctId:          "some-distinct-id",
			onlyEvaluateLocally: true,
		},
	))

	if matchedVariant != nil {
		t.Error("Should not match")
	}

	isMatch, _ := client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:                 "beta-feature",
			distinctId:          "some-distinct-id",
			onlyEvaluateLocally: true,
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	matchedVariant, _ = client.GetFeatureFlag(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:                 "beta-feature2",
			distinctId:          "some-distinct-id",
			onlyEvaluateLocally: true,
		},
	))

	if matchedVariant != nil {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:                 "beta-feature2",
			distinctId:          "some-distinct-id",
			onlyEvaluateLocally: true,
		},
	))

	if isMatch {
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

	isMatch, _ := client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:        "false-flag",
			distinctId: "some-distinct-id",
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:        "false-flag",
			distinctId: "some-distinct-id",
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:        "false-flag-2",
			distinctId: "some-distinct-id",
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:        "false-flag-2",
			distinctId: "some-distinct-id",
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

}

func TestFeatureFlagDefaultsComeIntoPlayOnlyWhenDecideErrorsOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{ads}"))
	}))

	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:           "test-get-feature",
			distinctId:    "distinct_id",
			defaultResult: false,
		},
	))

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:           "test-get-feature",
			distinctId:    "distinct_id",
			defaultResult: true,
		},
	))

	if !isMatch {
		t.Error("Should match")
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

	featureVariant, _ := client.GetFeatureFlag(makeFeatureFlagConfig(
		FeatureFlagConfig{
			key:        "beta-feature",
			distinctId: "distinct_id",
		},
	))

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, false)

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, false)

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, false)

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, true)

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, true)

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

	featureVariants, _ = client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{}, true)

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
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:        "simple-flag",
				distinctId: "distinct-id",
			},
		),
	)
	if !isMatch {
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
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:        "test-get-feature",
				distinctId: "distinct_id",
			},
		),
	)

	if variant != "variant-1" {
		t.Error("Should match")
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
		makeFeatureFlagConfig(
			FeatureFlagConfig{
				key:        "test-get-feature",
				distinctId: "distinct_id",
			},
		),
	)

	if variant != "variant-1" {
		t.Error("Should match")
	}

}

func TestCaptureMultipleUsersDoesntOutOfMemory(t *testing.T) {

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
			makeFeatureFlagConfig(
				FeatureFlagConfig{
					key:        "simple-flag",
					distinctId: fmt.Sprintf("%s%d", "distinct_id_", i),
				},
			),
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

	for i := 0; i < 5; i++ {

		variant, _ := client.GetFeatureFlag(
			makeFeatureFlagConfig(
				FeatureFlagConfig{
					key:        "multivariate-flag",
					distinctId: fmt.Sprintf("%s%d", "distinct_id_", i),
				},
			),
		)
		if results[i] != variant {
			t.Error("Match result is not consistent")
		}
	}
}
