package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComplexCohortsLocally(t *testing.T) {
	client := newCohortTestClient(t, "feature_flag/test-complex-cohorts-locally.json")
	payload := cohortPayload(NewProperties().Set("region", "UK"))

	assertFeatureEnabled(t, client, payload, false)
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	assertFeatureEnabled(t, client, payload, true)

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	assertFeatureEnabled(t, client, payload, true)
}

func TestComplexCohortsWithNegationLocally(t *testing.T) {
	client := newCohortTestClient(t, "feature_flag/test-complex-cohorts-negation-locally.json")
	payload := cohortPayload(NewProperties().Set("region", "UK"))

	assertFeatureEnabled(t, client, payload, false)

	// even though 'other' property is not present, the cohort should still match since it's an OR condition
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("nation", "UK")
	assertFeatureEnabled(t, client, payload, true)

	// # since 'other' is negated, we return False. Since 'nation' is not present, we can't tell whether the flag should be true or false, so go to decide
	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing")
	if _, err := client.IsFeatureEnabled(payload); err != nil {
		t.Error("Expected to fail")
	}

	payload.PersonProperties = NewProperties().Set("region", "USA").Set("other", "thing2")
	assertFeatureEnabled(t, client, payload, true)
}

func newCohortTestClient(t *testing.T, fixtureName string) Client {
	return newFeatureFlagsFixtureClient(t, fixtureName)
}

func cohortPayload(properties Properties) FeatureFlagPayload {
	return FeatureFlagPayload{Key: "beta-feature", DistinctId: "some-distinct-id", PersonProperties: properties}
}

func assertFeatureEnabled(t *testing.T, client Client, payload FeatureFlagPayload, want bool) {
	t.Helper()
	isMatch, err := client.IsFeatureEnabled(payload)
	if err != nil {
		t.Fatal(err)
	}
	if isMatch != want {
		t.Errorf("IsFeatureEnabled = %v, want %v", isMatch, want)
	}
}

// A cohort leaf whose person property was not passed in is inconclusive. It must not
// decide the cohort locally (as a mismatch in an AND group, or through negation as a
// match in an OR group); the flag has to fall back to the /flags API instead.
func TestCohortWithMissingPropertyFallsBackToAPI(t *testing.T) {
	tests := []struct {
		name   string
		cohort string
		// The API knows the stored person properties, so its answer differs from
		// the one the SDK would guess from the partial local context.
		apiValue bool
	}{
		{
			name:     "AND group with a missing property",
			cohort:   `{"type":"AND","values":[{"key":"plan","operator":"exact","value":["pro"],"type":"person"},{"key":"country","operator":"exact","value":["US"],"type":"person"}]}`,
			apiValue: true,
		},
		{
			name:     "OR group with a negated missing property",
			cohort:   `{"type":"OR","values":[{"key":"plan","operator":"exact","value":["enterprise"],"type":"person","negation":true}]}`,
			apiValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definitions := `{"flags":[{"id":1,"key":"beta-feature","active":true,"filters":{"groups":[{"properties":[{"key":"id","value":1,"type":"cohort"}],"rollout_percentage":100}]}}],"cohorts":{"1":` + tt.cohort + `}}`
			var flagsCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
					w.Write([]byte(definitions))
					return
				}
				if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
					flagsCalls.Add(1)
					fmt.Fprintf(w, `{"flags":{"beta-feature":{"key":"beta-feature","enabled":%t}}}`, tt.apiValue)
				}
			}))
			defer server.Close()

			client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{SecretKey: "some very secret key", Endpoint: server.URL})
			require.NoError(t, err)
			defer client.Close()

			value, err := client.GetFeatureFlag(FeatureFlagPayload{
				Key:              "beta-feature",
				DistinctId:       "some-distinct-id",
				PersonProperties: NewProperties().Set("country", "US"),
			})
			require.NoError(t, err)
			require.Equal(t, tt.apiValue, value)
			require.Equal(t, int32(1), flagsCalls.Load(), "an inconclusive cohort must fall back to /flags")
		})
	}
}
