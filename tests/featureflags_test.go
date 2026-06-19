package posthog

import (
	"math"
	"testing"
)

func TestCalculateHash(t *testing.T) {
	// calculateHash(key, distinctId, salt) computes SHA1(key + "." + distinctId + salt)
	// The "." is appended internally.
	for _, tt := range []struct {
		key   string
		ident string
		want  float64
	}{
		{"holdout-", "some_distinct_id", 0.0866397292395582},
		{"holdout-", "test-identifier", 0.7496340887209227},
		{"holdout-", "example_id", 0.8691395133214396},
		{"holdout-", "example_id2", 0.8442736553863017},
	} {
		t.Run(tt.ident, func(t *testing.T) {
			got := calculateHash(tt.key, tt.ident, "")
			if math.Abs(got-tt.want) > 0.000001 {
				t.Logf("got: %.16f, want: %f", got, tt.want)
				t.Fail()
			}
		})
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
