package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func TestSameGroupFlagDependency(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		for _, aggregation := range []string{"flag", "condition"} {
			for _, shape := range []string{"direct", "cohort", "nested", "indirect"} {
				t.Run(fmt.Sprintf("v%d/%s/%s", version, aggregation, shape), func(t *testing.T) {
					flagAggregation, conditionAggregation := `"aggregation_group_type_index":0,`, ""
					if aggregation == "condition" {
						flagAggregation, conditionAggregation = "", flagAggregation
					}
					leaf := `{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"dependency_chain":["dep"]}`
					targetProperty := leaf
					if shape == "cohort" || shape == "nested" {
						targetProperty = `{"type":"cohort","value":"outer"}`
					} else if shape == "indirect" {
						targetProperty = `{"type":"flag","key":"middle","operator":"flag_evaluates_to","value":true,"dependency_chain":["dep","middle"]}`
					}
					cohort := fmt.Sprintf(`{"type":"OR","values":[%s]}`, leaf)
					if shape == "nested" {
						cohort = `{"type":"AND","values":[{"type":"OR","values":[{"type":"cohort","value":"inner"}]}]}`
					}
					body := fmt.Sprintf(`{"property_matching_version":%d,"group_type_mapping":{"0":"company"},"flags":[
      {"key":"dep","active":true,"filters":{"aggregation_group_type_index":0,"groups":[{"rollout_percentage":50,"properties":[{"key":"plan","type":"group","operator":"exact","value":"pro"},{"key":"$group_key","type":"group","operator":"exact","value":"acme"}]}]}},
      {"key":"middle","active":true,"filters":{"aggregation_group_type_index":0,"groups":[{"properties":[%s]}]}},
      {"key":"target","active":true,"filters":{%s"groups":[{%s"properties":[%s]}],"payloads":{"true":"on","false":"off"}}}
     ],"cohorts":{"outer":%s,"inner":{"type":"OR","values":[%s]}}}`, version, leaf, flagAggregation, conditionAggregation, targetProperty, cohort, leaf)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/flags/definitions" {
							t.Errorf("same-group dependency made remote request: %s", r.URL.Path)
							w.WriteHeader(500)
							return
						}
						fmt.Fprint(w, body)
					}))
					defer server.Close()
					poller := newTestPoller(t, server.URL)
					noRetries := 0
					var err error
					poller.decider, err = newFlagsClient("test-key", server.URL, http.Client{}, time.Second, poller.Logger, &noRetries)
					if err != nil {
						t.Fatal(err)
					}
					poller.firstFeatureFlagRequestFinished = make(chan bool)
					poller.fetchNewFeatureFlags()
					close(poller.firstFeatureFlagRequestFinished)
					// Choose a person on the opposite side of the rollout from the group.
					wantRollout := checkIfSimpleFlagEnabled("dep", "acme", 50)
					person := ""
					for i := 0; i < 100; i++ {
						candidate := fmt.Sprintf("person-%d", i)
						if checkIfSimpleFlagEnabled("dep", candidate, 50) != wantRollout {
							person = candidate
							break
						}
					}
					if person == "" {
						t.Fatal("no contrasting person rollout fixture")
					}
					for _, plan := range []string{"pro", "free"} {
						want := plan == "pro" && wantRollout
						config := FeatureFlagPayload{Key: "dep", DistinctId: person, PersonProperties: Properties{"plan": "free"}, Groups: Groups{"company": "acme"}, GroupProperties: map[string]Properties{"company": {"plan": plan}}, OnlyEvaluateLocally: true}
						direct, local, err := poller.GetFeatureFlag(config)
						if err != nil || !local || direct != want {
							t.Fatalf("control dep=%v local=%v err=%v; want %v", direct, local, err, want)
						}
						config.Key = "target"
						for _, onlyLocal := range []bool{true, false} {
							config.OnlyEvaluateLocally = onlyLocal
							got, local, err := poller.GetFeatureFlag(config)
							if err != nil || !local || got != want {
								t.Errorf("plan=%s onlyLocal=%v target=%v local=%v err=%v; want %v", plan, onlyLocal, got, local, err, want)
							}
						}
						config.OnlyEvaluateLocally = true
						full := poller.GetFeatureFlagWithPayload(config)
						if full.err != nil || !full.locallyEvaluated || full.value != want {
							t.Errorf("full=%+v; want %v", full, want)
						}
						all, err := poller.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: person, Groups: config.Groups, PersonProperties: config.PersonProperties, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
						if err != nil || all["target"] != want {
							t.Errorf("bulk=%v err=%v; want target=%v", all, err, want)
						}
						c := &client{Config: Config{Logger: newDefaultLogger(false)}, featureFlagsPoller: poller}
						evaluations, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: person, Groups: config.Groups, PersonProperties: config.PersonProperties, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
						if err != nil {
							t.Fatal(err)
						}
						if _, exists := evaluations.flags["target"]; !exists {
							t.Error("EvaluateFlags omitted same-group target")
						}
						captured, err := poller.getFeatureFlagVariantsWithFallback(person, nil, config.Groups, config.PersonProperties, config.GroupProperties, true)
						if err != nil || captured["target"] != want {
							t.Errorf("capture=%v err=%v; want target=%v", captured, err, want)
						}
					}
				})
			}
		}
	}
}

func TestGroupFlagDependencyRejectsDifferentAggregationBeforeCache(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, aggregation := range []string{"flag", "condition"} {
			t.Run(fmt.Sprintf("v%d/%s", version, aggregation), func(t *testing.T) {
				flagAggregation, conditionAggregation := `"aggregation_group_type_index":1,`, ""
				if aggregation == "condition" {
					// Same flag-level aggregation, but a condition needs another group.
					flagAggregation, conditionAggregation = `"aggregation_group_type_index":0,`, `"aggregation_group_type_index":1,`
				}
				body := fmt.Sprintf(`{"property_matching_version":%d,"group_type_mapping":{"0":"company","1":"team"},"flags":[
     {"key":"dep","active":true,"filters":{%s"groups":[{%s"properties":[{"type":"group","key":"plan","operator":"exact","value":"pro"}]}]}},
     {"key":"target","active":true,"filters":{"aggregation_group_type_index":0,"groups":[{"properties":[{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"dependency_chain":["dep"]}]}]}}
    ],"cohorts":{"c":{"type":"OR","values":[{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"negation":true,"dependency_chain":["dep"]}]}}}`, version, flagAggregation, conditionAggregation)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/flags/definitions" {
						t.Errorf("local-only made remote request: %s", r.URL.Path)
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
				config := FeatureFlagPayload{DistinctId: "person", Groups: Groups{"company": "acme", "team": "engineering"}, GroupProperties: map[string]Properties{"company": {"plan": "free"}, "team": {"plan": "pro"}}, OnlyEvaluateLocally: true}
				for _, key := range []string{"dep", "target"} {
					config.Key = key
					got, local, err := poller.GetFeatureFlag(config)
					if key == "dep" {
						if err != nil || !local || got != true {
							t.Fatalf("control=%v local=%v err=%v", got, local, err)
						}
					} else if !isServerEvalError(err) || local {
						t.Errorf("target=%v local=%v err=%v; must require server", got, local, err)
					}
				}
				var raw FeatureFlagsResponse
				if err := json.Unmarshal([]byte(body), &raw); err != nil {
					t.Fatal(err)
				}
				state := poller.state.Load()
				company := uint8(0)
				for _, cohorts := range []map[string]PropertyGroup{raw.Cohorts, state.cohorts} {
					for _, cached := range []interface{}{nil, false, true} {
						cache := map[string]interface{}{"dep": cached}
						for i := 0; i < 2; i++ {
							got, err := poller.matchCohort(FlagProperty{Value: "c"}, config.GroupProperties["company"], cohorts, state.flagsByKey, cache, "acme", nil, &company, state)
							if !isServerEvalError(err) {
								t.Errorf("negated cohort=%v err=%v cached=%v; must require server", got, err, cached)
							}
						}
					}
				}
			})
		}
	}
}
