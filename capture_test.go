package posthog

import (
	"encoding/json"
	"testing"
)

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

func TestCaptureAPIfyUsesCanonicalLibraryProperties(t *testing.T) {
	apiMsg, ok := Capture{Event: "test-event", DistinctId: "user-123"}.APIfy().(CaptureInApi)
	if !ok {
		t.Fatalf("expected CaptureInApi, got %T", apiMsg)
	}

	for _, tt := range []struct {
		key  string
		want interface{}
	}{
		{key: "$lib", want: SDKName},
		{key: "$lib_version", want: getVersion()},
	} {
		t.Run(tt.key, func(t *testing.T) {
			if got := apiMsg.Properties[tt.key]; got != tt.want {
				t.Errorf("%s: got %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestCaptureAPIfyOmitsIgnoredTopLevelFieldsFromJSON(t *testing.T) {
	apiMsg := Capture{
		Type:             "capture",
		Event:            "test-event",
		DistinctId:       "user-123",
		SendFeatureFlags: SendFeatureFlags(true),
	}.APIfy()

	data, err := json.Marshal(apiMsg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var wire map[string]interface{}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, key := range []string{"type", "library", "library_version", "send_feature_flags"} {
		if _, ok := wire[key]; ok {
			t.Errorf("%s should not be serialized on capture events", key)
		}
	}

	props, ok := wire["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}
	if got := props["$lib"]; got != SDKName {
		t.Errorf("$lib: got %v, want %s", got, SDKName)
	}
	if got := props["$lib_version"]; got != getVersion() {
		t.Errorf("$lib_version: got %v, want %s", got, getVersion())
	}
}
