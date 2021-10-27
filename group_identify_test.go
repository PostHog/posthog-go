package posthog

import "testing"

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
	groupIdentify := GroupIdentify{}

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
