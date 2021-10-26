package posthog

import "testing"

func TestGroupIdentifyMissingDistinctId(t *testing.T) {
	groupIdentify := GroupIdentify{}

	if err := groupIdentify.Validate(); err == nil {
		t.Error("validating an invalid group identify object succeeded:", groupIdentify)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating group identify:", err)

	} else if e != (FieldError{
		Type:  "posthog.GroupIdentify",
		Name:  "DistinctId",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating group identify:", err)
	}
}

func TestGroupIdentifyValidWithDistinctId(t *testing.T) {
	groupIdentify := Identify{
		DistinctId: "organization_id:5",
	}

	if err := groupIdentify.Validate(); err != nil {
		t.Error("validating a valid identify object failed:", groupIdentify, err)
	}
}
