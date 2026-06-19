package posthog

import "testing"

func TestAliasMissingDistinctId(t *testing.T) {
	assertFieldError(t, Alias{Alias: "1"}, fieldError("posthog.Alias", "DistinctId"))
}

func TestAliasMissingAlias(t *testing.T) {
	assertFieldError(t, Alias{DistinctId: "1"}, fieldError("posthog.Alias", "Alias"))
}

func TestAliasValid(t *testing.T) {
	assertValid(t, Alias{Alias: "1", DistinctId: "2"})
}

func TestAliasAPIfyIncludesIsServerProperty(t *testing.T) {
	assertIsServerProperty(t, Alias{Alias: "1", DistinctId: "2", IsServer: true}.APIfy(), true)
}

func TestAliasAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	assertIsServerProperty(t, Alias{Alias: "1", DistinctId: "2", IsServer: false}.APIfy(), false)
}
