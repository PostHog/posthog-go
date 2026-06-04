package posthog

import "testing"

func TestIdentifyMissingDistinctId(t *testing.T) {
	assertFieldError(t, Identify{}, fieldError("posthog.Identify", "DistinctId"))
}

func TestIdentifyValidWithDistinctId(t *testing.T) {
	assertValid(t, Identify{DistinctId: "2"})
}

func TestIdentifyAPIfyIncludesIsServerProperty(t *testing.T) {
	assertIsServerProperty(t, Identify{DistinctId: "user-123", IsServer: true}.APIfy(), true)
}

func TestIdentifyAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	assertIsServerProperty(t, Identify{DistinctId: "user-123", IsServer: false}.APIfy(), false)
}
