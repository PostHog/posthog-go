package posthog

import "testing"

func TestMatchPropertyValue(t *testing.T) {
	property := make(map[string]interface{})

	property["key"] = "Browser"
	property["value"] = "Chrome"
	property["operatpr"] = "exact"
	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if err != nil || !isMatch {
		t.Error("Value is not a match")
	}

}
func TestMatchPropertySlice(t *testing.T) {
	property := make(map[string]interface{})

	property["key"] = "Browser"
	property["value"] = []interface{}{"Chrome"}
	property["operatpr"] = "exact"
	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if err != nil || !isMatch {
		t.Error("Value is not a match")
	}

}
