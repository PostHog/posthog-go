package posthog

import (
	"encoding/json"
	"testing"
)

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

func aliasWireProps(t *testing.T, alias Alias) map[string]interface{} {
	t.Helper()
	jsonBytes, err := json.Marshal(alias.APIfy())
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

func TestAliasAPIfyIncludesIsServerProperty(t *testing.T) {
	props := aliasWireProps(t, Alias{Alias: "1", DistinctId: "2", IsServer: true})
	if got := props["$is_server"]; got != true {
		t.Errorf("$is_server: expected true, got %v", got)
	}
}

func TestAliasAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	props := aliasWireProps(t, Alias{Alias: "1", DistinctId: "2", IsServer: false})
	if _, present := props["$is_server"]; present {
		t.Errorf("$is_server should be absent when IsServer is false, got %v", props["$is_server"])
	}
}
