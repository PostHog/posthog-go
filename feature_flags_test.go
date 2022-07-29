package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"

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
	property := Property{
		Key:      "key",
		Value:    "\\.com$",
		Operator: "regex",
	}

	isMatch, err := matchProperty(property, NewProperties().Set("key", "value.com"))

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	isMatch, err = matchProperty(property, NewProperties().Set("key", ".com343tfvalue5"))

	if err != nil {
		t.Error(err)
	}

	if isMatch {
		t.Error("Value is not a match")
	}
}

func TestMatchPropertyContains(t *testing.T) {

}

func TestFallbackToDecide(t *testing.T) {

}

func TestLocalEvaluationPersonProperty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	isMatch, _ := client.IsFeatureEnabled("simple-flag", "some-distinct-id", false, NewProperties().Set("region", "USA"), NewProperties())

	if !isMatch {
		t.Error("Should match")
	}

	isMatch, _ = client.IsFeatureEnabled("simple-flag", "some-distinct-id", false, NewProperties().Set("region", "Canada"), NewProperties())

	if isMatch {
		t.Error("Should not match")
	}

}

func TestLocalEvaluationGroupProperty(t *testing.T) {

}

func TestExperienceContinuityOverride(t *testing.T) {

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
		isMatch, _ := client.IsFeatureEnabled("simple-flag", fmt.Sprintf("%s%d", "distinct_id_", i), false, NewProperties(), NewProperties())
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
		variant, _ := client.GetFeatureFlag("multivariate-flag", fmt.Sprintf("%s%d", "distinct_id_", i), false, NewProperties(), NewProperties())
		if results[i] != variant {
			t.Error("Match result is not consistent")
		}
	}
}
