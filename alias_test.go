package posthog

import "testing"

func TestAliasMissingDistinctId(t *testing.T) {
	alias := Alias{
		Alias: "1",
	}

	if err := alias.Validate(); err == nil {
		t.Error("validating an invalid alias object succeeded:", alias)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating alias:", err)

	} else if e != (FieldError{
		Type:  "posthog.Alias",
		Name:  "DistinctId",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating alias:", err)
	}
}

func TestAliasMissingAlias(t *testing.T) {
	alias := Alias{
		DistinctId: "1",
	}

	if err := alias.Validate(); err == nil {
		t.Error("validating an invalid alias object succeeded:", alias)

	} else if e, ok := err.(FieldError); !ok {
		t.Error("invalid error type returned when validating alias:", err)

	} else if e != (FieldError{
		Type:  "posthog.Alias",
		Name:  "Alias",
		Value: "",
	}) {
		t.Error("invalid error value returned when validating alias:", err)
	}
}

func TestAliasValid(t *testing.T) {
	alias := Alias{
		Alias:      "1",
		DistinctId: "2",
	}

	if err := alias.Validate(); err != nil {
		t.Error("validating a valid alias object failed:", alias, err)
	}
}
