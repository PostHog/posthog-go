package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
)

func TestCohortFlagDependencyGroupContext(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		for _, aggregation := range []string{"person", "flag", "condition"} {
			for _, shape := range []string{"OR", "AND", "negated", "nested", "indirect"} {
				t.Run(fmt.Sprintf("v%d/%s/%s", version, aggregation, shape), func(t *testing.T) {
					flagAggregation, conditionAggregation, propertyType := "", "", "person"
					if aggregation == "flag" {
						flagAggregation, propertyType = `"aggregation_group_type_index":0,`, "group"
					} else if aggregation == "condition" {
						conditionAggregation, propertyType = `"aggregation_group_type_index":0,`, "group"
					}
					leaf := `{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"dependency_chain":["dep"]}`
					cohort := fmt.Sprintf(`{"type":"OR","values":[%s]}`, leaf)
					switch shape {
					case "AND":
						cohort = fmt.Sprintf(`{"type":"AND","values":[%s]}`, leaf)
					case "negated":
						cohort = `{"type":"OR","values":[{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"negation":true,"dependency_chain":["dep"]}]}`
					case "nested":
						cohort = fmt.Sprintf(`{"type":"AND","values":[{"type":"OR","values":[%s]}]}`, leaf)
					case "indirect":
						cohort = `{"type":"AND","values":[{"type":"flag","key":"middle","operator":"flag_evaluates_to","value":true,"dependency_chain":["middle"]}]}`
					}
					body := fmt.Sprintf(`{"property_matching_version":%d,"group_type_mapping":{"0":"company"},"flags":[
					 {"key":"dep","active":true,"filters":{%s"groups":[{%s"properties":[{"key":"plan","type":%q,"operator":"exact","value":"pro"}]}]}},
					 {"key":"middle","active":true,"filters":{"groups":[{"properties":[%s]}]}},
					 {"key":"target","active":true,"filters":{"groups":[{"properties":[{"type":"cohort","key":"id","value":"c"}]}],"payloads":{"true":"on","false":"off"}}}
					],"cohorts":{"c":%s}}`, version, flagAggregation, conditionAggregation, propertyType, leaf, cohort)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/flags/definitions" {
							t.Errorf("local-only evaluation made a remote request: %s", r.URL.Path)
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
					properties := Properties{"plan": "free"}
					if aggregation == "person" {
						properties["plan"] = "pro"
					}
					config := FeatureFlagPayload{Key: "dep", DistinctId: "person", PersonProperties: properties, Groups: Groups{"company": "acme"}, GroupProperties: map[string]Properties{"company": {"plan": "pro"}}, OnlyEvaluateLocally: true}
					direct, local, err := poller.GetFeatureFlag(config)
					if err != nil || !local || direct != true {
						t.Fatalf("control dep=%v local=%v err=%v", direct, local, err)
					}
					config.Key = "target"
					want := shape != "negated"
					got, local, err := poller.GetFeatureFlag(config)
					full := poller.GetFeatureFlagWithPayload(config)
					if aggregation == "person" {
						if err != nil || !local || got != want || full.err != nil || !full.locallyEvaluated || full.value != want {
							t.Errorf("person dependency=%v local=%v err=%v full=%+v; want %v", got, local, err, full, want)
						}
					} else {
						if !isServerEvalError(err) || local || !isServerEvalError(full.err) || full.locallyEvaluated {
							t.Errorf("dependency=%v local=%v err=%v full=%+v; must require server evaluation", got, local, err, full)
						}
						if payload, err := poller.GetFeatureFlagPayload(config); err == nil || payload != "" {
							t.Errorf("payload=%q error=%v; must remain inconclusive", payload, err)
						}
						all, err := poller.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
						if _, exists := all["target"]; err != nil || exists {
							t.Errorf("bulk flags=%v err=%v; must omit target", all, err)
						}
						c := &client{Config: Config{Logger: newDefaultLogger(false)}, featureFlagsPoller: poller}
						evaluations, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
						if err != nil {
							t.Fatal(err)
						}
						if _, exists := evaluations.flags["target"]; exists {
							t.Error("EvaluateFlags must omit target")
						}
						captured, err := poller.getFeatureFlagVariantsWithFallback(config.DistinctId, nil, config.Groups, properties, config.GroupProperties, true)
						if _, exists := captured["target"]; err != nil || exists {
							t.Errorf("capture flags=%v err=%v; must omit target", captured, err)
						}
					}
					var raw FeatureFlagsResponse
					if err := json.Unmarshal([]byte(body), &raw); err != nil {
						t.Fatal(err)
					}
					state := poller.state.Load()
					for _, cohorts := range []map[string]PropertyGroup{raw.Cohorts, state.cohorts} {
						// Repeated references must preserve server-required errors, even
						// if a caller already cached a result without group context.
						cache := map[string]interface{}{}
						if aggregation != "person" && shape != "indirect" {
							cache["dep"] = false
						}
						for i := 0; i < 2; i++ {
							got, err := poller.matchCohort(FlagProperty{Value: "c"}, properties, cohorts, state.flagsByKey, cache, "person", nil, state)
							if aggregation == "person" {
								if err != nil || got != want {
									t.Errorf("cohort=%v err=%v; want %v", got, err, want)
								}
							} else if !isServerEvalError(err) {
								t.Errorf("cohort=%v err=%v; must require server evaluation on call %d", got, err, i)
							}
						}
					}
				})
			}
		}
	}
}
