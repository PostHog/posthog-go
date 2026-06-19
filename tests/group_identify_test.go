package posthog

import "testing"

func TestGroupIdentifyMissingType(t *testing.T) {
	assertFieldError(t, GroupIdentify{}, fieldError("posthog.GroupIdentify", "Type"))
}

func TestGroupIdentifyMissingKey(t *testing.T) {
	assertFieldError(t, GroupIdentify{Type: "organization"}, fieldError("posthog.GroupIdentify", "Key"))
}

func TestGroupIdentifyValidWithTypeAndKey(t *testing.T) {
	assertValid(t, GroupIdentify{Type: "organization", Key: "id:5"})
}

func TestGroupIdentifyAPIfyIncludesIsServerProperty(t *testing.T) {
	assertIsServerProperty(t, GroupIdentify{Type: "organization", Key: "id:5", IsServer: true}.APIfy(), true)
}

func TestGroupIdentifyAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	assertIsServerProperty(t, GroupIdentify{Type: "organization", Key: "id:5", IsServer: false}.APIfy(), false)
}
