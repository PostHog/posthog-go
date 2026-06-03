package posthog

import (
	"testing"
)

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

func TestCaptureAPIfyIncludesIsServerProperty(t *testing.T) {
	capture := Capture{
		Event:      "test-event",
		DistinctId: "user-123",
		IsServer:   true,
	}

	apiMsg, ok := capture.APIfy().(CaptureInApi)
	if !ok {
		t.Fatalf("expected CaptureInApi, got %T", capture.APIfy())
	}

	expectKeys := map[string]interface{}{
		"$lib":       SDKName,
		"$is_server": true,
	}
	for k, want := range expectKeys {
		if got := apiMsg.Properties[k]; got != want {
			t.Errorf("property %q: expected %v (%T), got %v (%T)", k, want, want, got, got)
		}
	}
}

func TestCaptureAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	capture := Capture{
		Event:      "test-event",
		DistinctId: "user-123",
		IsServer:   false,
	}

	apiMsg, ok := capture.APIfy().(CaptureInApi)
	if !ok {
		t.Fatalf("expected CaptureInApi, got %T", capture.APIfy())
	}

	if _, present := apiMsg.Properties["$is_server"]; present {
		t.Errorf("$is_server should be absent when IsServer is false, got %v", apiMsg.Properties["$is_server"])
	}
}
