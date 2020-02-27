package posthog

import "testing"

func TestCaptureMissingEvent(t *testing.T) {
	capture := Capture{
		DistinctId: "1",
	}

	if err := capture.Validate(); err == nil {
		t.Error("validating an invalid capture object succeeded:", capture)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating capture:", err)

	} else if e != (FieldError{
		Type:  "posthog.Capture",
		Name:  "Event",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating capture:", err)
	}
}

func TestCaptureMissingDistinctId(t *testing.T) {
	capture := Capture{
		Event: "1",
	}

	if err := capture.Validate(); err == nil {
		t.Error("validating an invalid capture object succeeded:", capture)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating capture:", err)

	} else if e != (FieldError{
		Type:  "posthog.Capture",
		Name:  "DistinctId",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating capture:", err)
	}
}

func TestCaptureValidWithDistinctId(t *testing.T) {
	capture := Capture{
		Event:      "1",
		DistinctId: "2",
	}

	if err := capture.Validate(); err != nil {
		t.Error("validating a valid capture object failed:", capture, err)
	}
}
