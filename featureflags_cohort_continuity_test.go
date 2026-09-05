package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
)

func TestCohortFlagDependencyExperienceContinuity(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		for _, continuity := range []bool{false, true} {
			t.Run(fmt.Sprintf("version=%d/continuity=%t", version, continuity), func(t *testing.T) {
				body := fmt.Sprintf(`{"property_matching_version":%d,"flags":[
				 {"key":"sticky","active":true,"ensure_experience_continuity":%t,"filters":{"groups":[{"properties":[]}]}},
				 {"key":"target","active":true,"filters":{"groups":[{"properties":[{"type":"cohort","key":"id","value":"c"}]}]}}
				],"cohorts":{"c":{"type":"OR","values":[{"type":"flag","key":"sticky","operator":"flag_evaluates_to","value":true,"dependency_chain":["sticky"]}]}}}`, version, continuity)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/flags/definitions" {
						t.Errorf("unexpected remote request: %s", r.URL.Path)
						w.WriteHeader(500)
						return
					}
					fmt.Fprint(w, body)
				}))
				defer server.Close()
				poller := newTestPoller(t, server.URL)
				poller.firstFeatureFlagRequestFinished = make(chan bool)
				poller.fetchNewFeatureFlags()
				close(poller.firstFeatureFlagRequestFinished)
				// The cohort dependency must remain inconclusive just like the direct flag.
				for _, key := range []string{"sticky", "target"} {
					config := FeatureFlagPayload{Key: key, DistinctId: "person", OnlyEvaluateLocally: true}
					got, local, err := poller.GetFeatureFlag(config)
					if continuity {
						if err == nil || local {
							t.Errorf("%s=%v local=%v err=%v; must remain inconclusive", key, got, local, err)
						}
						full := poller.GetFeatureFlagWithPayload(config)
						if full.err == nil || full.locallyEvaluated {
							t.Errorf("%s full result must remain inconclusive: %+v", key, full)
						}
					} else if err != nil || !local || got != true {
						t.Errorf("%s=%v local=%v err=%v; want local true", key, got, local, err)
					}
				}
				// Exercise raw hydration as well as the pre-parsed production path above.
				var raw FeatureFlagsResponse
				if err := json.Unmarshal([]byte(body), &raw); err != nil {
					t.Fatal(err)
				}
				state := poller.state.Load()
				cache := map[string]interface{}{}
				for i := 0; i < 2; i++ {
					got, err := poller.matchCohort(FlagProperty{Value: "c"}, nil, raw.Cohorts, state.flagsByKey, cache, "person", nil, false, state)
					if continuity {
						if err == nil {
							t.Errorf("raw cohort=%v; must remain inconclusive, including cached dependency", got)
						}
					} else if err != nil || !got {
						t.Errorf("raw cohort=%v err=%v; want true", got, err)
					}
				}
			})
		}
	}
}
