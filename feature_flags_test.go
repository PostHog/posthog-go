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

	isMatch, _ := client.IsFeatureEnabled("simple-flag", "some-distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

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

	isMatch, _ := client.IsFeatureEnabled("complex-flag", "some-distinct-id", false, Groups{}, NewProperties().Set("region", "USA").Set("name", "Aloha"), map[string]Properties{})

	if !isMatch {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled("complex-flag", "some-distinct-id_within_rollou", false, Groups{}, NewProperties().Set("region", "USA").Set("email", "a@b.com"), map[string]Properties{})

	if !isMatch {
		t.Error("Should match")
	}

}

func TestDefaultDoesntAffectEval(t *testing.T) {
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

	isMatch, _ := client.IsFeatureEnabled("false-flag", "some-distinct-id", true, Groups{}, NewProperties(), map[string]Properties{})

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled("false-flag", "some-distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled("false-flag-2", "some-distinct-id", true, Groups{}, NewProperties(), map[string]Properties{})

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled("false-flag-2", "some-distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

	if isMatch {
		t.Error("Should not match")
	}

}

func TestDefaultValueAfterError(t *testing.T) {

}

func TestLocalEvaluationPersonProperty(t *testing.T) {

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

	isMatch, _ := client.IsFeatureEnabled("simple-flag", "some-distinct-id", false, Groups{}, NewProperties().Set("region", "USA"), map[string]Properties{})

	if !isMatch {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled("simple-flag", "some-distinct-id", false, Groups{}, NewProperties().Set("region", "Canada"), map[string]Properties{})

	if isMatch {
		t.Error("Should not match")
	}
}

func TestLocalEvaluationGroupProperty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-flag-group-properties.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled("group-flag", "some-distinct-id", false, Groups{}, NewProperties(), map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")})

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled("group-flag", "some-distinct-id", false, Groups{}, NewProperties(), map[string]Properties{"company": NewProperties().Set("name", "Project Name 2")})

	if isMatch {
		t.Error("Should not match")
	}

	isMatch, _ = client.IsFeatureEnabled("group-flag", "some-distinct-id", false, Groups{"company": "amazon_without_rollout"}, NewProperties(), map[string]Properties{"company": NewProperties().Set("name", "Project Name 1")})

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

	featureVariant, _ := client.GetFeatureFlag("beta-feature", "distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

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

	featureVariants, _ := client.GetAllFlags("distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})

	if featureVariants["beta-feature"] != true || featureVariants["disabled-feature"] != false {
		t.Error("Should match")
	}
}

func TestSimpleFlagWithoutRollout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-simple-flag-without-rollout.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled("simple-flag", "distinct-id", false, Groups{}, NewProperties(), map[string]Properties{})
	if !isMatch {
		t.Error("Should be enabled")
	}
}

// TODO: investigate test slowness
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

	results := []bool{false, true, true, false, true}

	for i := 0; i < 5; i++ {
		isMatch, _ := client.IsFeatureEnabled("simple-flag", fmt.Sprintf("%s%d", "distinct_id_", i), false, Groups{}, NewProperties(), map[string]Properties{})
		if results[i] != isMatch {
			t.Error("Match result is not consistent")
		}
	}
}

// TODO: investigate test slowness
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

	results := []interface{}{"second-variant", "second-variant", "first-variant", false, false}

	for i := 0; i < 5; i++ {
		variant, _ := client.GetFeatureFlag("multivariate-flag", fmt.Sprintf("%s%d", "distinct_id_", i), false, Groups{}, NewProperties(), map[string]Properties{})
		if results[i] != variant {
			t.Error("Match result is not consistent")
		}
	}
}
