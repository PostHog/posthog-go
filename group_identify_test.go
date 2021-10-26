package posthog 

import "testing"

func TestGroupIdentifyBasic(t *testing.T) {
	groupIdentify := GroupIdentify{}

	if err := groupIdentify.Validate(); err == nil {
		t.Error("validating an invalid group identify object succeeded:", groupIdentify)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating group identify:", err)

	} else if e != (FieldError{
		Type:  "posthog.GroupIdentify",
		Name:  "DistinctId",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating group identify:", err)
	}
}

func TestGroupIdentifyAdvanced(t *testing.T) {

}