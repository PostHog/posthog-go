package posthog

import "testing"

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
