package posthog

import "testing"

func TestCaptureMissingEvent(t *testing.T) {
	assertFieldError(t, Capture{DistinctId: "1"}, fieldError("posthog.Capture", "Event"))
}

func TestCaptureMissingDistinctId(t *testing.T) {
	assertFieldError(t, Capture{Event: "1"}, fieldError("posthog.Capture", "DistinctId"))
}

func TestCaptureValidWithDistinctId(t *testing.T) {
	assertValid(t, Capture{Event: "1", DistinctId: "2"})
}

func TestCaptureAPIfyIncludesIsServerProperty(t *testing.T) {
	assertIsServerProperty(t, Capture{Event: "test-event", DistinctId: "user-123", IsServer: true}.APIfy(), true)
}

func TestCaptureAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	assertIsServerProperty(t, Capture{Event: "test-event", DistinctId: "user-123", IsServer: false}.APIfy(), false)
}
