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
					// The dependency is evaluated in its own context, so every aggregation
					// resolves locally with the value it has when evaluated directly.
					if err != nil || !local || got != want || full.err != nil || !full.locallyEvaluated || full.value != want {
						t.Errorf("dependency=%v local=%v err=%v full=%+v; want %v", got, local, err, full, want)
					}
					all, err := poller.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
					if err != nil || all["target"] != want {
						t.Errorf("bulk flags=%v err=%v; want target=%v", all, err, want)
					}
					c := &client{Config: Config{Logger: newDefaultLogger(false)}, featureFlagsPoller: poller}
					evaluations, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
					if err != nil {
						t.Fatal(err)
					}
					if record, exists := evaluations.flags["target"]; !exists || record.Enabled != want {
						t.Errorf("EvaluateFlags target=%+v exists=%v; want %v", record, exists, want)
					}
					captured, err := poller.getFeatureFlagVariantsWithFallback(config.DistinctId, nil, config.Groups, properties, config.GroupProperties, nil, true)
					if err != nil || captured["target"] != want {
						t.Errorf("capture flags=%v err=%v; want target=%v", captured, err, want)
					}
					var raw FeatureFlagsResponse
					if err := json.Unmarshal([]byte(body), &raw); err != nil {
						t.Fatal(err)
					}
					state := poller.state.Load()
					for _, cohorts := range []map[string]PropertyGroup{raw.Cohorts, state.cohorts} {
						inputs := &flagEvaluationInputs{distinctId: config.DistinctId, groups: config.Groups, personProperties: properties, groupProperties: config.GroupProperties, cohorts: cohorts}
						cache := map[string]interface{}{}
						for i := 0; i < 2; i++ {
							got, err := poller.matchCohort(FlagProperty{Value: "c"}, properties, nil, cohorts, state.flagsByKey, cache, inputs, nil, state)
							if err != nil || got != want {
								t.Errorf("cohort=%v err=%v; want %v on call %d", got, err, want, i)
							}
						}
					}
				})
			}
		}
	}
}

func TestGroupContextPersonFlagDependency(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, aggregation := range []string{"flag", "condition"} {
			for _, shape := range []string{"direct", "cohort", "nested", "negated"} {
				t.Run(fmt.Sprintf("v%d/%s/%s", version, aggregation, shape), func(t *testing.T) {
					flagAggregation, conditionAggregation := `"aggregation_group_type_index":0,`, ""
					if aggregation == "condition" {
						flagAggregation, conditionAggregation = "", `"aggregation_group_type_index":0,`
					}
					leaf := `{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"dependency_chain":["dep"]}`
					cohort := fmt.Sprintf(`{"type":"OR","values":[%s]}`, leaf)
					if shape == "nested" {
						cohort = `{"type":"AND","values":[{"type":"OR","values":[{"type":"cohort","value":"inner"}]}]}`
					} else if shape == "negated" {
						cohort = `{"type":"OR","values":[{"type":"flag","key":"dep","operator":"flag_evaluates_to","value":true,"negation":true,"dependency_chain":["dep"]}]}`
					}
					targetProperty := `{"type":"cohort","value":"c"}`
					if shape == "direct" {
						targetProperty = leaf
					}
					body := fmt.Sprintf(`{"property_matching_version":%d,"group_type_mapping":{"0":"company"},"flags":[
					 {"key":"dep","active":true,"filters":{"groups":[{"properties":[{"type":"person","key":"plan","operator":"exact","value":"free"}]}]}},
					 {"key":"target","active":true,"filters":{%s"groups":[{%s"properties":[%s]}]}},
					 {"key":"property-only","active":true,"filters":{%s"groups":[{%s"properties":[{"type":"cohort","value":"properties"}]}]}}
					],"cohorts":{"c":%s,"inner":{"type":"OR","values":[%s]},"properties":{"type":"AND","values":[{"type":"group","key":"plan","operator":"exact","value":"pro"}]}}}`, version, flagAggregation, conditionAggregation, targetProperty, flagAggregation, conditionAggregation, cohort, leaf)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/flags/definitions" {
							t.Errorf("local-only evaluation made remote request: %s", r.URL.Path)
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
					config := FeatureFlagPayload{DistinctId: "person", PersonProperties: Properties{"plan": "free"}, Groups: Groups{"company": "acme"}, GroupProperties: map[string]Properties{"company": {"plan": "pro"}}, OnlyEvaluateLocally: true}
					// The person dependency reads the person's plan, not the company's.
					want := shape != "negated"
					for _, key := range []string{"dep", "property-only", "target"} {
						config.Key = key
						got, local, err := poller.GetFeatureFlag(config)
						if key == "target" {
							if err != nil || !local || got != want {
								t.Errorf("target=%v local=%v err=%v; want local %v", got, local, err, want)
							}
						} else if err != nil || !local || got != true {
							t.Errorf("control %s=%v local=%v err=%v; want local true", key, got, local, err)
						}
					}
					var raw FeatureFlagsResponse
					if err := json.Unmarshal([]byte(body), &raw); err != nil {
						t.Fatal(err)
					}
					state := poller.state.Load()
					for _, cohorts := range []map[string]PropertyGroup{raw.Cohorts, state.cohorts} {
						groupIndex := uint8(0)
						inputs := &flagEvaluationInputs{distinctId: config.DistinctId, groups: config.Groups, personProperties: config.PersonProperties, groupProperties: config.GroupProperties, cohorts: cohorts}
						cache := map[string]interface{}{}
						for i := 0; i < 2; i++ {
							got, err := poller.matchCohort(FlagProperty{Value: "c"}, config.GroupProperties["company"], nil, cohorts, state.flagsByKey, cache, inputs, &groupIndex, state)
							if err != nil || got != want {
								t.Errorf("cohort=%v err=%v; want %v on call %d", got, err, want, i)
							}
						}
					}
				})
			}
		}
	}
}
