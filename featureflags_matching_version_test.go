package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
)

// Exercise the wire envelope and public local-only APIs, not just the matcher.
func TestPropertyMatchingVersionDefinitions(t *testing.T) {
	rows := []struct {
		name, filter, property string
		legacy, v2             bool
	}{
		{"false banana", `false`, `"banana"`, true, false},
		{"false zero", `false`, `0`, true, false},
		{"boolean list true", `["true","false"]`, `"true"`, false, true},
		{"boolean list pro", `["true","false"]`, `"pro"`, true, false},
		{"empty true", `[]`, `true`, true, true},
		{"empty array", `[]`, `[]`, true, true},
		{"true array", `true`, `[true]`, true, false},
		{"false uppercase", `false`, `"FALSE"`, true, true},
		{"false null", `false`, `null`, true, false},
		{"false empty string", `false`, `""`, true, false},
		{"true empty property array", `true`, `[]`, true, false},
		{"nested filter member", `[[true],"pro"]`, `[true]`, true, true},
		{"empty nested truthy", `[]`, `[true,"TRUE",[]]`, true, true},
		{"empty false", `[]`, `false`, false, false},
		{"empty number", `[]`, `1`, false, false},
		{"empty arbitrary string", `[]`, `"banana"`, false, false},
		{"mixed members", `[true,"pro"]`, `"TRUE"`, true, true},
		{"normalized strings", `["FREE","PRO"]`, `"pro"`, true, true},
		{"unicode lowercase", `"İ"`, `"i̇"`, true, true},
		{"null equality", `null`, `null`, true, true},
	}
	for _, version := range []string{"", `,"property_matching_version":1`, `,"property_matching_version":2`, `,"property_matching_version":3`, `,"property_matching_version":0`} {
		for _, row := range rows {
			for _, operator := range []string{"exact", "is_not"} {
				t.Run(version+"/"+row.name+"/"+operator, func(t *testing.T) {
					body := fmt.Sprintf(`{"flags":[{"key":"test","active":true,"filters":{"groups":[{"properties":[{"key":"value","type":"person","operator":%q,"value":%s}]}],"payloads":{"true":"on","false":"off"}}}]%s}`, operator, row.filter, version)
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
					var property interface{}
					if err := json.Unmarshal([]byte(row.property), &property); err != nil {
						t.Fatal(err)
					}
					config := FeatureFlagPayload{Key: "test", DistinctId: "person", PersonProperties: Properties{"value": property}, OnlyEvaluateLocally: true}
					want := row.legacy
					if version == `,"property_matching_version":2` {
						want = row.v2
					}
					if operator == "is_not" {
						want = !want
					}
					got, local, err := poller.GetFeatureFlag(config)
					if err != nil || !local || got != want {
						t.Fatalf("got %v local=%v err=%v; want %v", got, local, err, want)
					}
					full := poller.GetFeatureFlagWithPayload(config)
					payload := "off"
					if want {
						payload = "on"
					}
					if full.err != nil || !full.locallyEvaluated || full.value != want || full.payload != payload {
						t.Errorf("full result: %+v; want %v/%s", full, want, payload)
					}
					gotPayload, err := poller.GetFeatureFlagPayload(config)
					if err != nil || gotPayload != payload {
						t.Errorf("payload=%s err=%v", gotPayload, err)
					}
				})
			}
		}
	}
}

const matchingVersionDefinitions = `{
 "flags": [
  {"key":"person","active":true,"filters":{"groups":[{"properties":[{"key":"value","type":"person","operator":"exact","value":false}]}]}},
  {"key":"group","active":true,"filters":{"aggregation_group_type_index":0,"groups":[{"properties":[{"key":"value","type":"group","operator":"exact","value":false}]}]}},
  {"key":"mixed","active":true,"filters":{"groups":[{"aggregation_group_type_index":0,"properties":[{"key":"value","type":"group","operator":"exact","value":false}]}]}},
  {"key":"cohort","active":true,"filters":{"groups":[{"properties":[{"key":"id","type":"cohort","value":"outer"}]}]}},
  {"key":"dependency","active":true,"filters":{"groups":[{"properties":[{"key":"person","type":"flag","operator":"flag_evaluates_to","value":true,"dependency_chain":["person"]}]}]}},
  {"key":"cohort-dependency","active":true,"filters":{"groups":[{"properties":[{"key":"id","type":"cohort","value":"dependency"}]}]}}
 ],
 "group_type_mapping":{"0":"company"},
 "cohorts":{
  "outer":{"type":"AND","values":[{"type":"OR","values":[{"key":"id","type":"cohort","value":"inner"}]}]},
  "inner":{"type":"AND","values":[{"key":"value","type":"person","operator":"exact","value":false}]},
  "dependency":{"type":"AND","values":[{"key":"person","type":"flag","operator":"flag_evaluates_to","value":true,"dependency_chain":["person"]}]}
 }
 %s
}`

func TestPropertyMatchingVersionReloadAndPropagation(t *testing.T) {
	body := ""
	status := http.StatusOK
	etag := "initial"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flags/definitions" {
			t.Errorf("unexpected remote request: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	poller := newTestPoller(t, server.URL)
	poller.firstFeatureFlagRequestFinished = make(chan bool)
	close(poller.firstFeatureFlagRequestFinished)
	config := FeatureFlagPayloadNoKey{DistinctId: "person", PersonProperties: Properties{"value": "banana"}, Groups: Groups{"company": "acme"}, GroupProperties: map[string]Properties{"company": {"value": "banana"}}, OnlyEvaluateLocally: true}
	// Identical definitions, with only the selector changing. Dependency result
	// caches must not survive an evaluation, including a version-only reload.
	for _, step := range []struct {
		name, version string
		status        int
		malformed     bool
		want          bool
		storedVersion int
	}{
		{"missing", "", 200, false, true, 0},
		{"v1", `,"property_matching_version":1`, 200, false, true, 1},
		{"v2", `,"property_matching_version":2`, 200, false, false, 2},
		{"back to v1", `,"property_matching_version":1`, 200, false, true, 1},
		{"v2 again", `,"property_matching_version":2`, 200, false, false, 2},
		{"304 new etag", "", 304, false, false, 2},
		{"304 no etag", "", 304, false, false, 2},
		{"server failure", "", 500, false, false, 2},
		{"decode failure", "", 200, true, false, 2},
		{"omitted resets", "", 200, false, true, 0},
		{"unknown legacy", `,"property_matching_version":3`, 200, false, true, 3},
	} {
		t.Run(step.name, func(t *testing.T) {
			status = step.status
			body = fmt.Sprintf(matchingVersionDefinitions, step.version)
			if step.malformed {
				body = "{"
			}
			etag = step.name
			if step.name == "304 no etag" {
				etag = ""
			}
			oldState := poller.state.Load()
			poller.fetchNewFeatureFlags()
			state := poller.state.Load()
			if state == nil || state.propertyMatchingVersion != step.storedVersion {
				t.Fatalf("unexpected snapshot: %+v", state)
			}
			if (step.status == 500 || step.malformed || step.name == "304 no etag") && state != oldState {
				t.Error("unchanged response replaced snapshot")
			}
			if step.name == "304 new etag" && state.flagsEtag != etag {
				t.Error("304 did not update etag")
			}
			for _, flag := range state.featureFlags {
				got, local, err := poller.GetFeatureFlag(FeatureFlagPayload{Key: flag.Key, DistinctId: config.DistinctId, PersonProperties: config.PersonProperties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
				if err != nil || !local || got != step.want {
					t.Errorf("%s: got %v local=%v err=%v want=%v", flag.Key, got, local, err, step.want)
				}
			}
			all, err := poller.GetAllFlags(config)
			assertMatchingVersionFlags(t, all, err, step.want)
			assertMatchingVersionEvaluations(t, poller, config, step.want)
			for _, onlyLocal := range []bool{false, true} {
				captured, err := poller.getFeatureFlagVariantsWithFallback(config.DistinctId, nil, config.Groups, config.PersonProperties, config.GroupProperties, onlyLocal)
				assertMatchingVersionFlags(t, captured, err, step.want)
			}
			// Raw cohort hydration compatibility path must agree with pre-parsed cache.
			var raw FeatureFlagsResponse
			if err := json.Unmarshal([]byte(fmt.Sprintf(matchingVersionDefinitions, "")), &raw); err != nil {
				t.Fatal(err)
			}
			got, err := poller.matchCohort(FlagProperty{Value: "outer"}, config.PersonProperties, raw.Cohorts, state.flagsByKey, map[string]interface{}{}, "person", nil, state)
			if err != nil || got != step.want {
				t.Errorf("raw cohort got %v err=%v", got, err)
			}
		})
	}
}

func assertMatchingVersionFlags(t *testing.T, flags map[string]interface{}, err error, want bool) {
	t.Helper()
	if err != nil || len(flags) != 6 {
		t.Fatalf("flags=%v err=%v", flags, err)
	}
	for key, value := range flags {
		if value != want {
			t.Errorf("%s=%v want=%v", key, value, want)
		}
	}
}

func TestPropertyMatchingVersionSafeguards(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		for _, operator := range []string{"exact", "is_not"} {
			prop := FlagProperty{Key: "value", Value: false, Operator: operator}
			_, err := matchProperty(prop, Properties{}, version)
			if err != errMissingPropertyValue {
				t.Errorf("version %d %s missing error=%v", version, operator, err)
			}
			prop.Value = []interface{}{float64(1), "pro"}
			_, err = matchProperty(prop, Properties{"value": "1"}, version)
			if err != errAmbiguousExactNumber {
				t.Errorf("version %d %s numeric error=%v", version, operator, err)
			}
			got, err := matchProperty(prop, Properties{"value": "PRO"}, version)
			if err != nil || got != (operator == "exact") {
				t.Errorf("unambiguous member: got %v err=%v", got, err)
			}
			prop.Value = `{"a":[true,"İ"],"b":null}`
			got, err = matchProperty(prop, Properties{"value": map[string]interface{}{"b": nil, "a": []interface{}{true, "i̇"}}}, version)
			if err != nil || got != (operator == "exact") {
				t.Errorf("canonical composite: got %v err=%v", got, err)
			}
		}
	}
	got, err := matchProperty(FlagProperty{Key: "value", Value: false, Operator: "exact"}, Properties{"value": "banana"})
	if err != nil || !got {
		t.Fatalf("context-free helper lost legacy default: %v %v", got, err)
	}
}

func TestPreParsePGDependencyChain(t *testing.T) {
	for _, chain := range []interface{}{[]string{"person"}, []interface{}{"person"}} {
		pg := preParsePG(PropertyGroup{Type: "AND", Values: []any{map[string]any{"type": "flag", "key": "person", "operator": "flag_evaluates_to", "value": true, "dependency_chain": chain}}})
		got := pg.ParsedValues[0].Property.DependencyChain
		if len(got) != 1 || got[0] != "person" {
			t.Errorf("chain %#v parsed as %#v", chain, got)
		}
	}
	for _, chain := range []interface{}{nil, "person", []interface{}{1}, []interface{}{"person", nil}} {
		pg := preParsePG(PropertyGroup{Type: "AND", Values: []any{map[string]any{"dependency_chain": chain}}})
		if pg.ParsedValues[0].Property.DependencyChain != nil {
			t.Errorf("malformed chain %#v accepted", chain)
		}
	}
}

// A JSON property can run user code during normalization. Swap definitions there
// to deterministically simulate a poll completing in the middle of evaluation.
type matchingVersionSwapProperty struct{ swap func() }

func (p matchingVersionSwapProperty) MarshalJSON() ([]byte, error) {
	p.swap()
	return []byte(`"go"`), nil
}

func TestPropertyMatchingVersionSnapshotPinned(t *testing.T) {
	poller := &FeatureFlagsPoller{Logger: newDefaultLogger(false), firstFeatureFlagRequestFinished: make(chan bool)}
	close(poller.firstFeatureFlagRequestFinished)
	var definitions FeatureFlagsResponse
	if err := json.Unmarshal([]byte(fmt.Sprintf(matchingVersionDefinitions, `,"property_matching_version":1`)), &definitions); err != nil {
		t.Fatal(err)
	}
	for i := range definitions.Flags {
		flag := &definitions.Flags[i]
		for j := range flag.Filters.Groups {
			condition := &flag.Filters.Groups[j]
			condition.Properties = append([]FlagProperty{{Key: "gate", Value: "go", Operator: "exact"}}, condition.Properties...)
		}
		flag.Filters.Payloads = map[string]json.RawMessage{"true": json.RawMessage(`"on"`), "false": json.RawMessage(`"off"`)}
	}
	preDecodePayloads(definitions.Flags)
	legacy := &flagsState{featureFlags: definitions.Flags, flagsByKey: buildFlagsByKey(definitions.Flags), groups: *definitions.GroupTypeMapping, cohorts: preParseCohortValues(definitions.Cohorts), propertyMatchingVersion: 1}
	// A changed group mapping and missing cohorts/dependency index make accidental
	// re-reads of any component of the snapshot observable, not just the version.
	replacement := &flagsState{featureFlags: definitions.Flags, flagsByKey: map[string]FeatureFlag{}, groups: map[string]string{"0": "other"}, propertyMatchingVersion: 2}
	swaps := 0
	gate := matchingVersionSwapProperty{swap: func() { swaps++; poller.state.Store(replacement) }}
	properties := Properties{"gate": gate, "value": "banana"}
	config := FeatureFlagPayload{Key: "cohort-dependency", DistinctId: "person", PersonProperties: properties, Groups: Groups{"company": "acme"}, GroupProperties: map[string]Properties{"company": properties}, OnlyEvaluateLocally: true}
	for _, api := range []string{"value", "payload", "full", "all", "capture local", "capture default", "evaluations"} {
		t.Run(api, func(t *testing.T) {
			poller.state.Store(legacy)
			swaps = 0
			switch api {
			case "value":
				got, local, err := poller.GetFeatureFlag(config)
				if err != nil || !local || got != true {
					t.Errorf("got %v local=%v err=%v", got, local, err)
				}
			case "payload":
				got, err := poller.GetFeatureFlagPayload(config)
				if err != nil || got != "on" {
					t.Errorf("got %v err=%v", got, err)
				}
			case "full":
				got := poller.GetFeatureFlagWithPayload(config)
				if got.err != nil || !got.locallyEvaluated || got.value != true || got.payload != "on" {
					t.Errorf("got %+v", got)
				}
			case "evaluations":
				assertMatchingVersionEvaluations(t, poller, FeatureFlagPayloadNoKey{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true}, true)
			case "all":
				got, err := poller.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: config.DistinctId, PersonProperties: properties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
				assertMatchingVersionFlags(t, got, err, true)
			default:
				got, err := poller.getFeatureFlagVariantsWithFallback(config.DistinctId, nil, config.Groups, properties, config.GroupProperties, api == "capture local")
				assertMatchingVersionFlags(t, got, err, true)
			}
			if swaps == 0 || poller.state.Load() != replacement {
				t.Fatal("test did not swap definitions during matching")
			}
		})
	}
}

func assertMatchingVersionEvaluations(t *testing.T, poller *FeatureFlagsPoller, config FeatureFlagPayloadNoKey, want bool) {
	t.Helper()
	c := &client{featureFlagsPoller: poller}
	evaluations, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: config.DistinctId, PersonProperties: config.PersonProperties, Groups: config.Groups, GroupProperties: config.GroupProperties, OnlyEvaluateLocally: true})
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]interface{}{}
	for key, record := range evaluations.flags {
		if !record.LocallyEvaluated {
			t.Errorf("%s not locally evaluated", key)
		}
		flags[key] = record.Enabled
	}
	assertMatchingVersionFlags(t, flags, nil, want)
}
