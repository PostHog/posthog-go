package posthog

import "testing"

func TestRequiredFieldValidationOrder(t *testing.T) {
	tests := []struct {
		name string
		msg  validatableMessage
		want FieldError
	}{
		{"capture event before distinct id", Capture{}, fieldError("posthog.Capture", "Event")},
		{"alias distinct id before alias", Alias{}, fieldError("posthog.Alias", "DistinctId")},
		{"group identify type before key", GroupIdentify{}, fieldError("posthog.GroupIdentify", "Type")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertFieldError(t, tt.msg, tt.want)
		})
	}
}

func TestFeatureFlagPayloadValidationDefaultsAndErrors(t *testing.T) {
	t.Run("single flag validates key before distinct id", func(t *testing.T) {
		payload := FeatureFlagPayload{}
		assertConfigError(t, payload.validate(), ConfigError{Reason: "Feature Flag Key required", Field: "Key", Value: ""})
	})

	t.Run("single flag validates distinct id after key", func(t *testing.T) {
		payload := FeatureFlagPayload{Key: "flag-key"}
		assertConfigError(t, payload.validate(), ConfigError{Reason: "DistinctId required", Field: "Distinct Id", Value: ""})
	})

	t.Run("all flags validates distinct id", func(t *testing.T) {
		payload := FeatureFlagPayloadNoKey{}
		assertConfigError(t, payload.validate(), ConfigError{Reason: "DistinctId required", Field: "Distinct Id", Value: ""})
	})

	t.Run("nil send events defaults to true", func(t *testing.T) {
		payload := FeatureFlagPayload{Key: "flag-key", DistinctId: "user-id"}
		if err := payload.validate(); err != nil {
			t.Fatalf("validate failed: %v", err)
		}
		if payload.SendFeatureFlagEvents == nil || *payload.SendFeatureFlagEvents != true {
			t.Fatalf("SendFeatureFlagEvents = %v, want true", payload.SendFeatureFlagEvents)
		}
	})

	t.Run("explicit false send events is preserved", func(t *testing.T) {
		value := false
		payload := FeatureFlagPayloadNoKey{DistinctId: "user-id", SendFeatureFlagEvents: &value}
		if err := payload.validate(); err != nil {
			t.Fatalf("validate failed: %v", err)
		}
		if payload.SendFeatureFlagEvents != &value || *payload.SendFeatureFlagEvents != false {
			t.Fatalf("SendFeatureFlagEvents = %v, want original false pointer", payload.SendFeatureFlagEvents)
		}
	})
}

func TestPropertiesAndGroupsSetReturnReceiver(t *testing.T) {
	props := NewProperties()
	returnedProps := props.Set("key", "value")
	returnedProps.Set("other", 42)
	if props["key"] != "value" || props["other"] != 42 {
		t.Fatalf("Properties.Set did not mutate and return the receiver: %#v", props)
	}

	groups := NewGroups()
	returnedGroups := groups.Set("company", "acme")
	returnedGroups.Set("team", "sdk")
	if groups["company"] != "acme" || groups["team"] != "sdk" {
		t.Fatalf("Groups.Set did not mutate and return the receiver: %#v", groups)
	}
}

func TestFeatureFlagsPollerStateMapGetters(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	if got := poller.getCohorts(); len(got) != 0 {
		t.Fatalf("empty poller getCohorts() = %#v, want empty map", got)
	}
	if got := poller.getGroups(); len(got) != 0 {
		t.Fatalf("empty poller getGroups() = %#v, want empty map", got)
	}

	cohorts := map[string]PropertyGroup{"1": {Type: "AND"}}
	groups := map[string]string{"0": "company"}
	poller.state.Store(&flagsState{cohorts: cohorts, groups: groups})

	gotCohorts := poller.getCohorts()
	gotGroups := poller.getGroups()
	gotCohorts["2"] = PropertyGroup{Type: "OR"}
	gotGroups["1"] = "team"

	if cohorts["2"].Type != "OR" {
		t.Fatalf("getCohorts() did not return the stored map")
	}
	if groups["1"] != "team" {
		t.Fatalf("getGroups() did not return the stored map")
	}
}

func assertConfigError(t *testing.T, err error, want ConfigError) {
	t.Helper()
	got, ok := err.(ConfigError)
	if !ok {
		t.Fatalf("error = %T %[1]v, want ConfigError", err)
	}
	if got != want {
		t.Fatalf("ConfigError = %#v, want %#v", got, want)
	}
}
