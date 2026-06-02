package posthog

import (
	"encoding/json"
	"testing"
)

func TestIdentifyMissingDistinctId(t *testing.T) {
	identify := Identify{}

	if err := identify.Validate(); err == nil {
		t.Error("validating an invalid identify object succeeded:", identify)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating identify:", err)

	} else if e != (FieldError{
		Type:  "posthog.Identify",
		Name:  "DistinctId",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating identify:", err)
	}
}

func TestIdentifyValidWithDistinctId(t *testing.T) {
	identify := Identify{
		DistinctId: "2",
	}

	if err := identify.Validate(); err != nil {
		t.Error("validating a valid identify object failed:", identify, err)
	}
}

func TestIdentifyAPIfyIncludesIsServerProperty(t *testing.T) {
	identify := Identify{
		DistinctId: "user-123",
	}

	apiMsg, ok := identify.APIfy().(IdentifyInApi)
	if !ok {
		t.Fatalf("expected IdentifyInApi, got %T", identify.APIfy())
	}

	jsonBytes, err := json.Marshal(apiMsg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var wire map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	props, ok := wire["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}

	expectKeys := map[string]interface{}{
		"$lib":       SDKName,
		"$is_server": true,
	}
	for k, want := range expectKeys {
		if got := props[k]; got != want {
			t.Errorf("property %q: expected %v (%T), got %v (%T)", k, want, want, got, got)
		}
	}
}
