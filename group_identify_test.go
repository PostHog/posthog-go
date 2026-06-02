package posthog

import (
	"encoding/json"
	"testing"
)

func TestGroupIdentifyMissingType(t *testing.T) {
	groupIdentify := GroupIdentify{}

	if err := groupIdentify.Validate(); err == nil {
		t.Error("validating an invalid group identify object succeeded:", groupIdentify)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating group identify:", err)

	} else if e != (FieldError{
		Type:  "posthog.GroupIdentify",
		Name:  "Type",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating group identify:", err)
	}
}

func TestGroupIdentifyMissingKey(t *testing.T) {
	groupIdentify := GroupIdentify{Type: "organization"}

	if err := groupIdentify.Validate(); err == nil {
		t.Error("validating an invalid group identify object succeeded:", groupIdentify)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating group identify:", err)

	} else if e != (FieldError{
		Type:  "posthog.GroupIdentify",
		Name:  "Key",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating group identify:", err)
	}
}

func TestGroupIdentifyValidWithTypeAndKey(t *testing.T) {
	groupIdentify := GroupIdentify{
		Type: "organization",
		Key:  "id:5",
	}

	if err := groupIdentify.Validate(); err != nil {
		t.Error("validating a valid identify object failed:", groupIdentify, err)
	}
}

func TestGroupIdentifyAPIfyIncludesIsServerProperty(t *testing.T) {
	groupIdentify := GroupIdentify{
		Type:     "organization",
		Key:      "id:5",
		IsServer: true,
	}

	apiMsg, ok := groupIdentify.APIfy().(GroupIdentifyInApi)
	if !ok {
		t.Fatalf("expected GroupIdentifyInApi, got %T", groupIdentify.APIfy())
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

func TestGroupIdentifyAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	groupIdentify := GroupIdentify{
		Type:     "organization",
		Key:      "id:5",
		IsServer: false,
	}

	apiMsg, ok := groupIdentify.APIfy().(GroupIdentifyInApi)
	if !ok {
		t.Fatalf("expected GroupIdentifyInApi, got %T", groupIdentify.APIfy())
	}

	if _, present := apiMsg.Properties["$is_server"]; present {
		t.Errorf("$is_server should be absent when IsServer is false, got %v", apiMsg.Properties["$is_server"])
	}
}
