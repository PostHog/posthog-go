package posthog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type validatableMessage interface {
	Validate() error
}

func fieldError(typeName, fieldName string) FieldError {
	return FieldError{Type: typeName, Name: fieldName, Value: ""}
}

func assertFieldError(t *testing.T, msg validatableMessage, want FieldError) {
	t.Helper()
	err := msg.Validate()
	if err == nil {
		t.Fatalf("validating an invalid object succeeded: %#v", msg)
	}
	got, ok := err.(FieldError)
	if !ok {
		t.Fatalf("invalid error type returned: %v", err)
	}
	if got != want {
		t.Fatalf("invalid error value returned: got %#v, want %#v", got, want)
	}
}

func assertValid(t *testing.T, msg validatableMessage) {
	t.Helper()
	if err := msg.Validate(); err != nil {
		t.Fatalf("validating a valid object failed: %#v: %v", msg, err)
	}
}

func wireProperties(t *testing.T, apiMsg APIMessage) map[string]interface{} {
	t.Helper()
	jsonBytes, err := json.Marshal(apiMsg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	props, ok := wire["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}
	return props
}

func assertIsServerProperty(t *testing.T, apiMsg APIMessage, wantPresent bool) {
	t.Helper()
	props := wireProperties(t, apiMsg)
	if wantPresent {
		if got := props["$is_server"]; got != true {
			t.Errorf("$is_server: expected true, got %v", got)
		}
		return
	}
	if _, present := props["$is_server"]; present {
		t.Errorf("$is_server should be absent when IsServer is false, got %v", props["$is_server"])
	}
}

// dereferenceMessage has a hand-written arm per message type, and Enqueue falls
// through to "custom types cannot be enqueued" if one is missing, so cover every
// arm directly rather than through a fixture.
func TestDereferenceMessage(t *testing.T) {
	cases := []struct {
		name string
		ptr  Message
		want Message
	}{
		{"alias", &Alias{Alias: "a", DistinctId: "b"}, Alias{Alias: "a", DistinctId: "b"}},
		{"identify", &Identify{DistinctId: "b"}, Identify{DistinctId: "b"}},
		{"groupIdentify", &GroupIdentify{Type: "org", Key: "k"}, GroupIdentify{Type: "org", Key: "k"}},
		{"capture", &Capture{Event: "e", DistinctId: "d"}, Capture{Event: "e", DistinctId: "d"}},
		{"exception", &Exception{DistinctId: "d"}, Exception{DistinctId: "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, dereferenceMessage(tc.ptr))
		})
	}

	t.Run("typed nil pointers become nil", func(t *testing.T) {
		for _, msg := range []Message{
			(*Alias)(nil), (*Identify)(nil), (*GroupIdentify)(nil), (*Capture)(nil), (*Exception)(nil),
		} {
			require.Nil(t, dereferenceMessage(msg))
		}
	})

	t.Run("values pass through unchanged", func(t *testing.T) {
		in := Capture{Event: "e", DistinctId: "d"}
		require.Equal(t, in, dereferenceMessage(in))
	})
}
