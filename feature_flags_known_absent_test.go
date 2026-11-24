package posthog

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

const knownAbsentGroupTypeMapping = `{"0": "organization", "1": "project"}`

func knownAbsentDefinitions(flags ...string) string {
	return `{"flags": [` + strings.Join(flags, ",") + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping + `, "cohorts": {}}`
}

func computeKnownAbsentFlag(t *testing.T, definitions, key string, groups Groups, personProperties Properties, groupProperties map[string]Properties, knownAbsent *knownAbsentPropertyKeys) (interface{}, error) {
	t.Helper()
	poller := newDefinitionsPoller(t, definitions)
	state := poller.state.Load()
	flag, ok := state.flagsByKey[key]
	require.True(t, ok, "no definition for %s", key)
	return poller.computeFlagLocally(flag, "user-123", nil, groups, personProperties, groupProperties, knownAbsent, state.cohorts, state)
}

func TestMatchPropertyWithKnownAbsentOperatorTable(t *testing.T) {
	listed := propertyKeySet{"plan": {}}
	for _, tt := range []struct {
		operator string
		value    interface{}
		want     bool
	}{
		{"is_not_set", nil, true},
		{"is_set", nil, false},
		{"exact", "pro", false},
		{"is_not", "pro", true},
		{"icontains", "pro", false},
		{"not_icontains", "pro", true},
		{"starts_with", "pro", false},
		{"not_starts_with", "pro", true},
		{"ends_with", "pro", false},
		{"not_ends_with", "pro", true},
		{"regex", "^pro", false},
		{"not_regex", "^pro", true},
		{"gt", "10", false},
		{"gte", "10", false},
		{"lt", "10", false},
		{"lte", "10", false},
		{"semver_eq", "1.2.3", false},
		{"semver_neq", "1.2.3", false},
		{"semver_gt", "1.2.3", false},
		{"semver_gte", "1.2.3", false},
		{"semver_lt", "1.2.3", false},
		{"semver_lte", "1.2.3", false},
		{"semver_tilde", "1.2.3", false},
		{"semver_caret", "1.2.3", false},
		{"semver_wildcard", "1.2.*", false},
		{"is_date_before", "2025-01-01T10:00:00Z", false},
		{"is_date_after", "2025-01-01T10:00:00Z", false},
	} {
		property := FlagProperty{Key: "plan", Operator: tt.operator, Value: tt.value, Type: "person"}
		for name, properties := range map[string]Properties{
			"omitted":  {},
			"supplied": {"plan": "pro"},
		} {
			t.Run(tt.operator+"/"+name, func(t *testing.T) {
				got, err := matchPropertyWithKnownAbsent(property, properties, listed)
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			})
		}
	}
}

func TestMatchPropertyWithKnownAbsentKeepsDefinitionChecksFirst(t *testing.T) {
	listed := propertyKeySet{"plan": {}}
	for _, operator := range []string{"icontains_multi", "not_icontains_multi", "between", "not_between", "future_operator"} {
		t.Run(operator, func(t *testing.T) {
			_, err := matchPropertyWithKnownAbsent(FlagProperty{Key: "plan", Operator: operator, Value: []interface{}{"pro"}}, Properties{}, listed)
			requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
		})
	}

}

// knownAbsentUnreadConditionValues pairs operators with condition values that local
// evaluation can't parse, compile or compare. A known-absent key resolves without
// reading them, as the flags service does.
var knownAbsentUnreadConditionValues = []struct {
	name     string
	operator string
	value    interface{}
	want     bool
}{
	{"not_regex invalid", "not_regex", "[", true},
	{"regex invalid", "regex", "[", false},
	{"not_regex lookahead", "not_regex", "(?=pro)", true},
	{"regex lookahead", "regex", "(?=pro)", false},
	{"not_regex unicode class", "not_regex", `\p{L}`, true},
	{"regex unicode class", "regex", `\p{L}`, false},
	{"is_not integer", "is_not", float64(0), true},
	{"exact integer", "exact", float64(1), false},
	{"gt", "gt", "not-a-number", false},
	{"gte", "gte", "not-a-number", false},
	{"lt", "lt", "not-a-number", false},
	{"lte", "lte", "not-a-number", false},
	{"semver_eq", "semver_eq", "not-a-version", false},
	{"semver_neq", "semver_neq", "not-a-version", false},
	{"semver_gt", "semver_gt", "not-a-version", false},
	{"semver_gte", "semver_gte", "not-a-version", false},
	{"semver_lt", "semver_lt", "not-a-version", false},
	{"semver_lte", "semver_lte", "not-a-version", false},
	{"semver_tilde", "semver_tilde", "not-a-version", false},
	{"semver_caret", "semver_caret", "not-a-version", false},
	{"semver_wildcard", "semver_wildcard", "not-a-version", false},
	{"is_date_before", "is_date_before", "not-a-date", false},
	{"is_date_after", "is_date_after", "not-a-date", false},
}

func TestKnownAbsentResolvesWithoutReadingTheConditionValue(t *testing.T) {
	flags := make([]string, 0, len(knownAbsentUnreadConditionValues))
	for i, tt := range knownAbsentUnreadConditionValues {
		value, err := json.Marshal(tt.value)
		require.NoError(t, err)
		filter := fmt.Sprintf(`{"key": "plan", "type": "person", "operator": %q, "value": %s}`, tt.operator, value)
		flags = append(flags, flagDefinition(fmt.Sprintf("flag-%d", i), flagCondition(100, filter)))
	}
	definitions := knownAbsentDefinitions(flags...)

	for i, tt := range knownAbsentUnreadConditionValues {
		t.Run(tt.name, func(t *testing.T) {
			for _, properties := range []Properties{nil, {"plan": "pro"}} {
				got, err := computeKnownAbsentFlag(t, definitions, fmt.Sprintf("flag-%d", i), nil, properties, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}

	server := newKnownAbsentServer(t, definitions)
	c, _, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
	waitForFlagDefinitions(t, c)

	t.Run("local only", func(t *testing.T) {
		evals, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: []string{"plan"}, OnlyEvaluateLocally: true})
		require.NoError(t, err)
		require.Empty(t, evals.UnresolvedFlags())
		for i, tt := range knownAbsentUnreadConditionValues {
			require.Equal(t, tt.want, evals.GetFlag(fmt.Sprintf("flag-%d", i)), tt.name)
		}
	})
	t.Run("default getter", func(t *testing.T) {
		for i, tt := range knownAbsentUnreadConditionValues {
			got, err := c.GetFeatureFlag(FeatureFlagPayload{Key: fmt.Sprintf("flag-%d", i), DistinctId: "user-1", PersonPropertyKeysKnownAbsent: []string{"plan"}})
			require.NoError(t, err)
			require.Equal(t, tt.want, got, tt.name)
		}
	})
	require.Empty(t, server.requests(), "no /flags request")
}

func TestKnownAbsentLocalEvaluation(t *testing.T) {
	planNotSet := flagDefinition("profile-incomplete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "is_not_set"}`))
	planSet := flagDefinition("profile-complete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "is_set"}`))
	definitions := knownAbsentDefinitions(planNotSet, planSet)

	t.Run("listed key matches is_not_set", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("listed key does not match is_set", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, definitions, "profile-complete", nil, Properties{"plan": "pro"}, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
		require.NoError(t, err)
		require.Equal(t, false, got)
	})
	t.Run("listed key overrides a supplied value", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, Properties{"plan": "pro"}, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("omitted key that is not listed stays inconclusive", func(t *testing.T) {
		_, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"region"}, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("person keys do not apply to group filters", func(t *testing.T) {
		_, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, nil, nil, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"plan"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("listed key overrides the injected distinct_id", func(t *testing.T) {
		defs := knownAbsentDefinitions(flagDefinition("no-distinct-id", flagCondition(100, `{"key": "distinct_id", "type": "person", "operator": "is_not_set"}`)))
		got, err := computeKnownAbsentFlag(t, defs, "no-distinct-id", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"distinct_id"}, nil))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("unsupported operator on a listed key still falls back", func(t *testing.T) {
		defs := knownAbsentDefinitions(flagDefinition("multi", flagCondition(100, `{"key": "plan", "type": "person", "operator": "not_icontains_multi", "value": ["pro"]}`)))
		_, err := computeKnownAbsentFlag(t, defs, "multi", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
	})
}

func TestKnownAbsentOmittedOrNullConditionValueStaysInconclusive(t *testing.T) {
	listed := newKnownAbsentPropertyKeys([]string{"plan"}, nil)
	for name, filter := range map[string]string{
		"is_not omitted": `{"key": "plan", "type": "person", "operator": "is_not"}`,
		"is_not null":    `{"key": "plan", "type": "person", "operator": "is_not", "value": null}`,
		"exact null":     `{"key": "plan", "type": "person", "operator": "exact", "value": null}`,
		"gt null":        `{"key": "plan", "type": "person", "operator": "gt", "value": null}`,
	} {
		t.Run(name, func(t *testing.T) {
			definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, filter)))
			for _, properties := range []Properties{nil, {"plan": "pro"}} {
				_, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, properties, nil, listed)
				requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
			}

			server := newKnownAbsentServer(t, definitions)
			c, _, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
			waitForFlagDefinitions(t, c)
			got, err := c.GetFeatureFlag(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: []string{"plan"}})
			require.NoError(t, err)
			require.Equal(t, true, got, "the /flags result")
			require.Len(t, server.requests(), 1, "falls back to /flags")
		})
	}

	for operator, want := range map[string]bool{"is_not_set": true, "is_set": false} {
		t.Run(operator+" null keeps the table result", func(t *testing.T) {
			definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "`+operator+`", "value": null}`)))
			got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, Properties{"plan": "pro"}, nil, listed)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestKnownAbsentPersonMetadataKeyIsIgnored(t *testing.T) {
	knownAbsent := newKnownAbsentPropertyKeys([]string{"__posthog_person_metadata__created_at", "plan"}, nil)
	require.NotContains(t, knownAbsent.personSet(), "__posthog_person_metadata__created_at")
	// The /flags body still carries the caller's list unchanged.
	require.Equal(t, []string{"__posthog_person_metadata__created_at", "plan"}, knownAbsent.wirePersonKeys())

	defs := knownAbsentDefinitions(flagDefinition("early-user", flagCondition(100, `{"key": "created_at", "type": "person_metadata", "operator": "is_date_before", "value": "2026-01-01"}`)))
	for _, listedKeys := range [][]string{{"__posthog_person_metadata__created_at"}, {"created_at"}} {
		got, err := computeKnownAbsentFlag(t, defs, "early-user", nil, Properties{"created_at": "2025-06-01"}, nil, newKnownAbsentPropertyKeys(listedKeys, nil))
		require.NoError(t, err, "listed %v", listedKeys)
		require.Equal(t, true, got, "listed %v", listedKeys)
	}
}

func TestKnownAbsentGroupKeysApplyOnlyToTheirGroupType(t *testing.T) {
	orgIncomplete := `{"key": "org-incomplete", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
		flagCondition(100, `{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`) + `]}}`
	orgWithoutKey := `{"key": "org-without-key", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
		flagCondition(100, `{"key": "$group_key", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`) + `]}}`
	definitions := knownAbsentDefinitions(orgIncomplete, orgWithoutKey)
	groups := Groups{"organization": "acme", "project": "p1"}

	t.Run("listed for the flag's group type", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, definitions, "org-incomplete", groups, nil, map[string]Properties{"organization": {"tier": "gold"}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("listed for another group type", func(t *testing.T) {
		_, err := computeKnownAbsentFlag(t, definitions, "org-incomplete", groups, nil, nil, newKnownAbsentPropertyKeys(nil, map[string][]string{"project": {"tier"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("listed as a person key", func(t *testing.T) {
		_, err := computeKnownAbsentFlag(t, definitions, "org-incomplete", groups, nil, nil, newKnownAbsentPropertyKeys([]string{"tier"}, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("listed key overrides the injected $group_key", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, definitions, "org-without-key", groups, nil, nil, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"$group_key"}}))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("mixed-targeting group condition without group properties", func(t *testing.T) {
		mixed := `{"key": "mixed", "active": true, "filters": {"groups": [{"aggregation_group_type_index": 0, "rollout_percentage": 100, "properties": [{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}]}]}}`
		defs := knownAbsentDefinitions(mixed)
		got, err := computeKnownAbsentFlag(t, defs, "mixed", groups, nil, nil, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}))
		require.NoError(t, err)
		require.Equal(t, true, got)

		_, err = computeKnownAbsentFlag(t, defs, "mixed", groups, nil, nil, newKnownAbsentPropertyKeys(nil, map[string][]string{"project": {"tier"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
}

func TestKnownAbsentCohortLeafAppliesBeforeNegation(t *testing.T) {
	definitions := `{"flags": [` +
		flagDefinition("not-in-paid", flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`)) +
		`], "group_type_mapping": {}, "cohorts": {"1": {"type": "AND", "values": [{"type": "AND", "values": [{"key": "plan", "type": "person", "operator": "is_set", "negation": true}]}]}}}`

	got, err := computeKnownAbsentFlag(t, definitions, "not-in-paid", nil, Properties{"plan": "pro"}, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = computeKnownAbsentFlag(t, definitions, "not-in-paid", nil, Properties{"plan": "pro"}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, false, got)

	_, err = computeKnownAbsentFlag(t, definitions, "not-in-paid", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"region"}, nil))
	requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
}

func TestKnownAbsentCohortLeafChecksOperatorSupportFirst(t *testing.T) {
	for _, negation := range []bool{false, true} {
		t.Run(fmt.Sprintf("negation=%t", negation), func(t *testing.T) {
			definitions := `{"flags": [` +
				flagDefinition("plan-range", flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`)) +
				fmt.Sprintf(`], "group_type_mapping": {}, "cohorts": {"1": {"type": "AND", "values": [{"type": "AND", "values": [{"key": "plan", "type": "person", "operator": "not_between", "value": [1, 2], "negation": %t}]}]}}}`, negation)

			_, err := computeKnownAbsentFlag(t, definitions, "plan-range", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
			requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
		})
	}
}

func TestKnownAbsentFlagDependencyUsesTheSameListing(t *testing.T) {
	definitions := knownAbsentDefinitions(
		flagDefinition("profile-incomplete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "is_not_set"}`)),
		flagDefinition("onboarding", flagCondition(100, flagDependencyFilter("profile-incomplete", true, "profile-incomplete"))),
	)

	got, err := computeKnownAbsentFlag(t, definitions, "onboarding", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
	require.NoError(t, err)
	require.Equal(t, true, got)

	_, err = computeKnownAbsentFlag(t, definitions, "onboarding", nil, nil, nil, nil)
	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnresolvedDependency)
}

// knownAbsentServer serves local definitions and records the raw /flags request bodies.
type knownAbsentServer struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []map[string]json.RawMessage
}

func newKnownAbsentServer(t *testing.T, definitions string) *knownAbsentServer {
	t.Helper()
	s := &knownAbsentServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions") || strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			_, _ = w.Write([]byte(definitions))
		case strings.HasPrefix(r.URL.Path, "/flags"):
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode /flags request: %v", err)
			}
			s.mu.Lock()
			s.bodies = append(s.bodies, body)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"flags": {"profile-incomplete": {"key": "profile-incomplete", "enabled": true, "metadata": {"id": 1, "version": 1}}}}`))
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *knownAbsentServer) requests() []map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]json.RawMessage(nil), s.bodies...)
}

func TestKnownAbsentRemoteBody(t *testing.T) {
	// profile-incomplete is inconclusive for another reason (an unknown operator), so
	// every API falls back to /flags.
	definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, unknownOperatorFilter)))
	person := []string{"plan"}
	groupKeys := map[string][]string{"organization": {"tier"}}

	type evaluate func(c Client, person []string, groups map[string][]string) error
	apis := map[string]evaluate{
		"GetFeatureFlag": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetFeatureFlag(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"GetFeatureFlagResult": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetFeatureFlagResult(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"GetAllFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"EvaluateFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"Capture.SendFeatureFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.(*client).getFeatureVariantsWithOptions("user-1", nil, nil, nil, &SendFeatureFlagsOptions{PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
	}

	for _, mode := range []string{"local fallback", "remote only"} {
		for name, call := range apis {
			if mode == "remote only" && name == "Capture.SendFeatureFlags" {
				continue // capture enrichment requires local evaluation
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				server := newKnownAbsentServer(t, definitions)
				c, _, _ := newEvalClient(t, server.server, func(cfg *Config) {
					if mode == "local fallback" {
						cfg.PersonalApiKey = "personal-key"
					}
				})
				if mode == "local fallback" {
					waitForFlagDefinitions(t, c)
				}

				require.NoError(t, call(c, person, groupKeys))
				require.NoError(t, call(c, nil, map[string][]string{"organization": {"tier"}, "project": nil, "team": {}}))
				require.NoError(t, call(c, nil, nil))
				require.NoError(t, call(c, []string{}, map[string][]string{}))
				require.NoError(t, call(c, nil, map[string][]string{"project": nil}))

				requests := server.requests()
				require.Len(t, requests, 5)
				require.JSONEq(t, `["plan"]`, string(requests[0]["person_property_keys_known_absent"]))
				require.JSONEq(t, `{"organization": ["tier"]}`, string(requests[0]["group_property_keys_known_absent"]))
				require.NotContains(t, requests[1], "person_property_keys_known_absent")
				require.JSONEq(t, `{"organization": ["tier"]}`, string(requests[1]["group_property_keys_known_absent"]), "empty group lists are left out")
				for _, request := range requests[2:] {
					require.NotContains(t, request, "person_property_keys_known_absent")
					require.NotContains(t, request, "group_property_keys_known_absent")
				}
			})
		}
	}
}

func TestKnownAbsentLimitValidation(t *testing.T) {
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "dup" // duplicates count toward the limit
		}
		return out
	}
	definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, unknownOperatorFilter)))

	type evaluate func(c Client, person []string, groups map[string][]string) error
	apis := map[string]evaluate{
		"GetFeatureFlag": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetFeatureFlag(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"IsFeatureEnabled": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.IsFeatureEnabled(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"GetFeatureFlagPayload": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetFeatureFlagPayload(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"GetAllFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"EvaluateFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"Capture.SendFeatureFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.(*client).getFeatureVariantsWithOptions("user-1", nil, nil, nil, &SendFeatureFlagsOptions{PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"FeatureFlagsPoller.GetFeatureFlag": func(c Client, person []string, groups map[string][]string) error {
			_, _, err := c.(*client).featureFlagsPoller.GetFeatureFlag(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"FeatureFlagsPoller.GetFeatureFlagPayload": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.(*client).featureFlagsPoller.GetFeatureFlagPayload(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
		"FeatureFlagsPoller.GetFeatureFlagWithPayload": func(c Client, person []string, groups map[string][]string) error {
			return c.(*client).featureFlagsPoller.GetFeatureFlagWithPayload(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups}).err
		},
		"FeatureFlagsPoller.GetAllFlags": func(c Client, person []string, groups map[string][]string) error {
			_, err := c.(*client).featureFlagsPoller.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: "user-1", PersonPropertyKeysKnownAbsent: person, GroupPropertyKeysKnownAbsent: groups})
			return err
		},
	}

	for name, call := range apis {
		t.Run(name, func(t *testing.T) {
			server := newKnownAbsentServer(t, definitions)
			c, _, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
			waitForFlagDefinitions(t, c)

			for _, input := range []struct {
				person []string
				groups map[string][]string
				field  string
			}{
				{keys(maxKnownAbsentPropertyKeys + 1), nil, "PersonPropertyKeysKnownAbsent"},
				{nil, map[string][]string{"organization": {"tier"}, "project": keys(maxKnownAbsentPropertyKeys + 1)}, "GroupPropertyKeysKnownAbsent"},
			} {
				err := call(c, input.person, input.groups)
				var configErr ConfigError
				require.True(t, errors.As(err, &configErr), "got %v", err)
				require.Equal(t, input.field, configErr.Field)
				require.Contains(t, configErr.Reason, fmt.Sprint(maxKnownAbsentPropertyKeys))
			}
			require.Empty(t, server.requests(), "an over-limit list must not reach /flags")

			require.NoError(t, call(c, keys(maxKnownAbsentPropertyKeys), map[string][]string{"organization": keys(maxKnownAbsentPropertyKeys)}))
			requests := server.requests()
			require.Len(t, requests, 1)
			var sent []string
			require.NoError(t, json.Unmarshal(requests[0]["person_property_keys_known_absent"], &sent))
			require.Len(t, sent, maxKnownAbsentPropertyKeys, "lists are sent without truncation or deduplication")
		})
	}
}

func TestKnownAbsentCaptureOverLimitSendsEventWithoutFlags(t *testing.T) {
	definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, unknownOperatorFilter)))
	server := newKnownAbsentServer(t, definitions)
	c, capture, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
	waitForFlagDefinitions(t, c)

	tooMany := make([]string, maxKnownAbsentPropertyKeys+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("key-%d", i)
	}
	require.NoError(t, c.Enqueue(Capture{
		Event:            "signed_up",
		DistinctId:       "user-1",
		SendFeatureFlags: &SendFeatureFlagsOptions{PersonPropertyKeysKnownAbsent: tooMany},
	}))

	events := waitForEventCount(capture, 1, 5*time.Second)
	require.Len(t, events, 1)
	require.Equal(t, "signed_up", events[0].Event)
	require.NotContains(t, events[0].Properties, "$feature/profile-incomplete")
	require.Empty(t, server.requests(), "an over-limit list must not reach /flags")
}

func TestKnownAbsentCohortLeafKeepsItsGroupScope(t *testing.T) {
	cohortFlag := `{"key": "org-cohort", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
		flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`) + `]}}`
	definitions := func(leaf string) string {
		return `{"flags": [` + cohortFlag + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping +
			`, "cohorts": {"1": {"type": "AND", "values": [{"type": "AND", "values": [` + leaf + `]}]}}}`
	}
	groups := Groups{"organization": "acme", "project": "p1"}
	orgTierAbsent := newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}})

	t.Run("leaf for another group type stays inconclusive", func(t *testing.T) {
		defs := definitions(`{"key": "tier", "type": "group", "group_type_index": 1, "operator": "is_not_set"}`)
		_, err := computeKnownAbsentFlag(t, defs, "org-cohort", groups, nil, map[string]Properties{"organization": {}}, orgTierAbsent)
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("person leaf stays inconclusive", func(t *testing.T) {
		defs := definitions(`{"key": "tier", "type": "person", "operator": "is_not_set"}`)
		_, err := computeKnownAbsentFlag(t, defs, "org-cohort", groups, nil, map[string]Properties{"organization": {}}, orgTierAbsent)
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("leaf for the flag's group type resolves", func(t *testing.T) {
		defs := definitions(`{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`)
		got, err := computeKnownAbsentFlag(t, defs, "org-cohort", groups, nil, map[string]Properties{"organization": {}}, orgTierAbsent)
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
}
