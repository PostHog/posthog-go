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
