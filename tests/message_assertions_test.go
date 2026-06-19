package posthog

import (
	"encoding/json"
	"testing"
)

type validatableMessage interface {
	Validate() error
}

func fieldError(typeName, fieldName string) FieldError {
	return FieldError{Type: typeName, Name: fieldName, Value: ""}
}

func assertFieldError(t *testing.T, msg validatableMessage, want FieldError) {
	t.Helper()
	err := msg.Validate()
	if err == nil {
		t.Fatalf("validating an invalid object succeeded: %#v", msg)
	}
	got, ok := err.(FieldError)
	if !ok {
		t.Fatalf("invalid error type returned: %v", err)
	}
	if got != want {
		t.Fatalf("invalid error value returned: got %#v, want %#v", got, want)
	}
}

func assertValid(t *testing.T, msg validatableMessage) {
	t.Helper()
	if err := msg.Validate(); err != nil {
		t.Fatalf("validating a valid object failed: %#v: %v", msg, err)
	}
}

func wireProperties(t *testing.T, apiMsg APIMessage) map[string]interface{} {
	t.Helper()
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
	return props
}

func assertIsServerProperty(t *testing.T, apiMsg APIMessage, wantPresent bool) {
	t.Helper()
	props := wireProperties(t, apiMsg)
	if wantPresent {
		if got := props["$is_server"]; got != true {
			t.Errorf("$is_server: expected true, got %v", got)
		}
		return
	}
	if _, present := props["$is_server"]; present {
		t.Errorf("$is_server should be absent when IsServer is false, got %v", props["$is_server"])
	}
}
