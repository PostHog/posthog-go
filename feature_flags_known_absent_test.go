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
	conditionValues := map[string]string{"omitted": "", "null": `, "value": null`}
	for _, operator := range []string{"exact", "is_not", "icontains", "gt", "is_date_before"} {
		for valueName, value := range conditionValues {
			t.Run(operator+" "+valueName, func(t *testing.T) {
				filter := `{"key": "plan", "type": "person", "operator": "` + operator + `"` + value + `}`
				definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, filter)))
				for _, properties := range []Properties{nil, {"plan": "pro"}} {
					_, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, properties, nil, listed)
					requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
				}

				server := newKnownAbsentServer(t, definitions)
				c, _, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
				waitForFlagDefinitions(t, c)
				for _, properties := range []Properties{nil, {"plan": "pro"}} {
					evals, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1", PersonProperties: properties, PersonPropertyKeysKnownAbsent: []string{"plan"}, OnlyEvaluateLocally: true})
					require.NoError(t, err)
					require.Equal(t, map[string]UnresolvedFlagReason{"profile-incomplete": UnresolvedFlagReasonUnsupportedDefinition}, evals.UnresolvedFlags())

					got, err := c.GetFeatureFlag(FeatureFlagPayload{Key: "profile-incomplete", DistinctId: "user-1", PersonProperties: properties, PersonPropertyKeysKnownAbsent: []string{"plan"}})
					require.NoError(t, err)
					require.Equal(t, true, got, "the /flags result")
				}
				require.Len(t, server.requests(), 2, "the default getter falls back to /flags")
			})
		}
	}

	for operator, want := range map[string]bool{"is_not_set": true, "is_set": false} {
		for valueName, value := range conditionValues {
			t.Run(operator+" "+valueName+" keeps the table result", func(t *testing.T) {
				definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "`+operator+`"`+value+`}`)))
				for _, properties := range []Properties{nil, {"plan": "pro"}} {
					got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, properties, nil, listed)
					require.NoError(t, err)
					require.Equal(t, want, got)
				}
			})
		}
	}

	for _, tt := range []struct {
		operator, value string
		want            bool
	}{
		{"exact", `"pro"`, false},
		{"is_not", `"pro"`, true},
		{"icontains", `"pro"`, false},
		{"gt", `"10"`, false},
		{"is_date_before", `"2024-01-01"`, false},
	} {
		t.Run(tt.operator+" with a value resolves from the listing", func(t *testing.T) {
			definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, `{"key": "plan", "type": "person", "operator": "`+tt.operator+`", "value": `+tt.value+`}`)))
			for _, properties := range []Properties{nil, {"plan": "pro"}} {
				got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, properties, nil, listed)
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

// Without a listing, an omitted or null condition value keeps the main-branch result.
func TestOmittedOrNullConditionValueWithoutListingIsUnchanged(t *testing.T) {
	for _, tt := range []struct {
		operator string
		want     interface{}
		wantErr  bool
	}{
		{"exact", false, false},
		{"is_not", true, false},
		{"icontains", false, false},
		{"gt", nil, true},
		{"is_date_before", nil, true},
		{"is_set", true, false},
		{"is_not_set", false, false},
	} {
		for valueName, value := range map[string]string{"omitted": "", "null": `, "value": null`} {
			t.Run(tt.operator+" "+valueName, func(t *testing.T) {
				filter := `{"key": "plan", "type": "person", "operator": "` + tt.operator + `"` + value + `}`
				definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, filter)))
				got, err := computeKnownAbsentFlag(t, definitions, "profile-incomplete", nil, Properties{"plan": "pro"}, nil, newKnownAbsentPropertyKeys([]string{"region"}, nil))
				if tt.wantErr {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			})
		}
	}
}

func TestKnownAbsentPersonMetadataKeyIsIgnored(t *testing.T) {
	knownAbsent := newKnownAbsentPropertyKeys([]string{"__posthog_person_metadata__created_at", "plan"}, nil)
	require.NotContains(t, knownAbsent.personSet(), "__posthog_person_metadata__created_at")
	// The /flags body still carries the caller's list unchanged.
	require.Equal(t, []string{"__posthog_person_metadata__created_at", "plan"}, knownAbsent.wirePersonKeys())

	defs := knownAbsentDefinitions(flagDefinition("early-user", flagCondition(100, `{"key": "created_at", "type": "person_metadata", "operator": "is_date_before", "value": "2026-01-01"}`)))
	// A person_metadata filter needs /flags, whatever the listing says.
	for _, listedKeys := range [][]string{{"__posthog_person_metadata__created_at"}, {"created_at"}} {
		_, err := computeKnownAbsentFlag(t, defs, "early-user", nil, Properties{"created_at": "2025-06-01"}, nil, newKnownAbsentPropertyKeys(listedKeys, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
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

func TestKnownAbsentCaptureOverLimitSendsEventWithoutFlagValues(t *testing.T) {
	tooMany := make([]string, maxKnownAbsentPropertyKeys+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("key-%d", i)
	}
	for name, opts := range map[string]*SendFeatureFlagsOptions{
		"person list": {PersonPropertyKeysKnownAbsent: tooMany},
		"group list":  {GroupPropertyKeysKnownAbsent: map[string][]string{"organization": tooMany}},
	} {
		t.Run(name, func(t *testing.T) {
			definitions := knownAbsentDefinitions(flagDefinition("profile-incomplete", flagCondition(100, unknownOperatorFilter)))
			server := newKnownAbsentServer(t, definitions)
			c, capture, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
			waitForFlagDefinitions(t, c)

			require.NoError(t, c.Enqueue(Capture{
				Event:            "signed_up",
				DistinctId:       "user-1",
				Groups:           Groups{"organization": "acme"},
				SendFeatureFlags: opts,
			}))

			events := waitForEventCount(capture, 1, 5*time.Second)
			require.Len(t, events, 1)
			require.Equal(t, "signed_up", events[0].Event)
			require.NotContains(t, events[0].Properties, "$feature/profile-incomplete")
			require.Equal(t, []string{}, events[0].Properties["$active_feature_flags"],
				"an over-limit list still attaches an empty $active_feature_flags")
			require.Empty(t, server.requests(), "an over-limit list must not reach /flags")
		})
	}
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

func TestKnownAbsentNeverHidesAPersonMetadataField(t *testing.T) {
	cohortDefinitions := func(filter string) string {
		return `{"flags": [` + flagDefinition("metadata-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`)) +
			`], "group_type_mapping": {}, "cohorts": {"1": {"type": "AND", "values": [{"type": "AND", "values": [` + filter + `]}]}}}`
	}
	flagDefinitions := func(filter string) string {
		return knownAbsentDefinitions(flagDefinition("metadata-flag", flagCondition(100, filter)))
	}
	listed := newKnownAbsentPropertyKeys([]string{"created_at"}, nil)

	for level, definitions := range map[string]func(string) string{"flag filter": flagDefinitions, "cohort leaf": cohortDefinitions} {
		// A person_metadata filter needs /flags, so a listed key never resolves it locally.
		for _, tt := range []struct {
			operator string
			value    string
		}{
			{"is_not_set", `null`},
			{"is_set", `null`},
			{"is_date_before", `"2026-01-01"`},
			{"is_date_after", `"2026-01-01"`},
		} {
			defs := definitions(fmt.Sprintf(`{"key": "created_at", "type": "person_metadata", "operator": %q, "value": %s}`, tt.operator, tt.value))
			for name, personProperties := range map[string]Properties{"value supplied": {"created_at": "2025-06-01"}, "value not supplied": nil} {
				t.Run(level+"/"+tt.operator+"/"+name, func(t *testing.T) {
					_, err := computeKnownAbsentFlag(t, defs, "metadata-flag", nil, personProperties, nil, listed)
					requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
				})
			}
		}
	}
}

func TestKnownAbsentKeysApplyOnlyToTheirFilterType(t *testing.T) {
	groups := Groups{"organization": "acme"}

	t.Run("person listing and a group filter in a person condition", func(t *testing.T) {
		defs := knownAbsentDefinitions(flagDefinition("person-flag", flagCondition(100, `{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`)))
		_, err := computeKnownAbsentFlag(t, defs, "person-flag", groups, nil, nil, newKnownAbsentPropertyKeys([]string{"tier"}, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("group listing and a person filter in a group condition", func(t *testing.T) {
		orgFlag := `{"key": "org-flag", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
			flagCondition(100, `{"key": "tier", "type": "person", "operator": "is_not_set"}`) + `]}}`
		_, err := computeKnownAbsentFlag(t, knownAbsentDefinitions(orgFlag), "org-flag", groups, nil, map[string]Properties{"organization": {}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("group listing and a person filter in a mixed-targeting group condition", func(t *testing.T) {
		mixed := `{"key": "mixed", "active": true, "filters": {"groups": [{"aggregation_group_type_index": 0, "rollout_percentage": 100, "properties": [{"key": "tier", "type": "person", "operator": "is_not_set"}]}]}}`
		_, err := computeKnownAbsentFlag(t, knownAbsentDefinitions(mixed), "mixed", groups, nil, map[string]Properties{"organization": {}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("person listing and a filter without a type", func(t *testing.T) {
		defs := knownAbsentDefinitions(flagDefinition("untyped", flagCondition(100, `{"key": "plan", "operator": "is_not_set"}`)))
		_, err := computeKnownAbsentFlag(t, defs, "untyped", nil, nil, nil, newKnownAbsentPropertyKeys([]string{"plan"}, nil))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
}

func TestKnownAbsentFlagDependencyAcrossAggregationsUsesItsOwnListing(t *testing.T) {
	orgIncomplete := `{"key": "org-incomplete", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
		flagCondition(100, `{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`) + `]}}`
	personOnGroup := flagDefinition("person-on-group", flagCondition(100, flagDependencyFilter("org-incomplete", true, "org-incomplete")))
	profileIncomplete := flagDefinition("profile-incomplete", flagCondition(100, `{"key": "tier", "type": "person", "operator": "is_not_set"}`))
	groupOnPerson := `{"key": "group-on-person", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
		flagCondition(100, flagDependencyFilter("profile-incomplete", true, "profile-incomplete")) + `]}}`
	definitions := knownAbsentDefinitions(orgIncomplete, personOnGroup, profileIncomplete, groupOnPerson)
	groups := Groups{"organization": "acme"}
	bothListed := newKnownAbsentPropertyKeys([]string{"tier"}, map[string][]string{"organization": {"tier"}})

	for _, key := range []string{"person-on-group", "group-on-person"} {
		t.Run(key, func(t *testing.T) {
			got, err := computeKnownAbsentFlag(t, definitions, key, groups, nil, map[string]Properties{"organization": {}}, bothListed)
			require.NoError(t, err)
			require.Equal(t, true, got)
		})
	}
	// Each dependency reads only the listing for its own aggregation.
	for key, listing := range map[string]*knownAbsentPropertyKeys{
		"person-on-group": newKnownAbsentPropertyKeys([]string{"tier"}, nil),
		"group-on-person": newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}),
	} {
		t.Run(key+"/listing for the dependent's aggregation only", func(t *testing.T) {
			_, err := computeKnownAbsentFlag(t, definitions, key, groups, nil, map[string]Properties{"organization": {}}, listing)
			requireUnresolvedReason(t, err, UnresolvedFlagReasonUnresolvedDependency)
		})
	}
}

func TestKnownAbsentGroupFilterWithoutIndexUsesConditionGroupType(t *testing.T) {
	groups := Groups{"organization": "acme"}
	enterprise := map[string]Properties{"organization": {"tier": "enterprise"}}
	orgTierAbsent := newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}})
	for _, tt := range []struct {
		name   string
		filter string
		want   bool
	}{
		{"exact", `{"key": "tier", "type": "group", "operator": "exact", "value": "enterprise"}`, false},
		{"is_not_set", `{"key": "tier", "type": "group", "operator": "is_not_set"}`, true},
	} {
		t.Run("condition filter/"+tt.name, func(t *testing.T) {
			legacyTier := `{"key": "legacy-tier", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` + flagCondition(100, tt.filter) + `]}}`
			got, err := computeKnownAbsentFlag(t, knownAbsentDefinitions(legacyTier), "legacy-tier", groups, nil, enterprise, orgTierAbsent)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
		t.Run("cohort leaf/"+tt.name, func(t *testing.T) {
			legacyTier := `{"key": "legacy-tier", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
				flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`) + `]}}`
			definitions := `{"flags": [` + legacyTier + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping +
				`, "cohorts": {"1": {"type": "AND", "values": [{"type": "AND", "values": [` + tt.filter + `]}]}}}`
			got, err := computeKnownAbsentFlag(t, definitions, "legacy-tier", groups, nil, enterprise, orgTierAbsent)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	for _, index := range []string{`256`, `-1`, `"0"`} {
		t.Run("cohort leaf with an unusable group_type_index "+index, func(t *testing.T) {
			legacyTier := `{"key": "legacy-tier", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
				flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`) + `]}}`
			definitions := `{"flags": [` + legacyTier + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping +
				`, "cohorts": {"1": {"type": "AND", "values": [{"key": "tier", "type": "group", "group_type_index": ` + index + `, "operator": "is_not_set"}]}}}`
			got, err := computeKnownAbsentFlag(t, definitions, "legacy-tier", groups, nil, enterprise, orgTierAbsent)
			require.NoError(t, err)
			require.Equal(t, false, got, "the supplied value decides, not the listing")
		})
	}
	t.Run("cohort leaf with a null group_type_index", func(t *testing.T) {
		legacyTier := `{"key": "legacy-tier", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` +
			flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`) + `]}}`
		definitions := `{"flags": [` + legacyTier + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping +
			`, "cohorts": {"1": {"type": "AND", "values": [{"key": "tier", "type": "group", "group_type_index": null, "operator": "is_not_set"}]}}}`
		got, err := computeKnownAbsentFlag(t, definitions, "legacy-tier", groups, nil, enterprise, orgTierAbsent)
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("cohort leaf in a person condition", func(t *testing.T) {
		personFlag := flagDefinition("person-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 1}`))
		definitions := `{"flags": [` + personFlag + `], "group_type_mapping": ` + knownAbsentGroupTypeMapping +
			`, "cohorts": {"1": {"type": "AND", "values": [{"key": "tier", "type": "group", "operator": "is_not_set"}]}}}`
		_, err := computeKnownAbsentFlag(t, definitions, "person-flag", groups, nil, enterprise, orgTierAbsent)
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
}

// orgFlagDefinition is a flag aggregated by the organization group type of knownAbsentGroupTypeMapping.
func orgFlagDefinition(key string, conditions ...string) string {
	return `{"key": "` + key + `", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [` + strings.Join(conditions, ",") + `]}}`
}

func TestFlagDependencyIsEvaluatedInItsOwnContext(t *testing.T) {
	const regionIsUS = `"key": "region", "operator": "exact", "value": "us"`
	personChild := flagDefinition("person-child", flagCondition(100, flagDependencyFilter("org-parent", true, "org-parent")))
	evaluate := func(t *testing.T, definitions string, payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, int32) {
		t.Helper()
		client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, remoteNoFlags)
		payload.DistinctId = "user-123"
		snapshot, err := client.EvaluateFlags(payload)
		require.NoError(t, err)
		return snapshot, remoteCalls.Load()
	}

	t.Run("a group dependency reads its group properties", func(t *testing.T) {
		orgParent := orgFlagDefinition("org-parent", flagCondition(100, `{`+regionIsUS+`, "type": "group", "group_type_index": 0}`))
		snapshot, remoteCalls := evaluate(t, knownAbsentDefinitions(orgParent, personChild), EvaluateFlagsPayload{
			FlagKeys:         []string{"person-child", "org-parent"},
			Groups:           Groups{"organization": "acme"},
			PersonProperties: Properties{"region": "us"},
			GroupProperties:  map[string]Properties{"organization": {"region": "eu"}},
		})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, false, snapshot.GetFlag("org-parent"))
		require.Equal(t, false, snapshot.GetFlag("person-child"))
	})

	t.Run("a group dependency without its group is false", func(t *testing.T) {
		orgParent := orgFlagDefinition("org-parent", flagCondition(100, `{"key": "tier", "type": "group", "group_type_index": 0, "operator": "is_not_set"}`))
		personChildExpectsFalse := flagDefinition("person-child-expects-false", flagCondition(100, flagDependencyFilter("org-parent", false, "org-parent")))
		definitions := knownAbsentDefinitions(orgParent, personChild, personChildExpectsFalse)
		orgTierAbsent := map[string][]string{"organization": {"tier"}}

		snapshot, remoteCalls := evaluate(t, definitions, EvaluateFlagsPayload{FlagKeys: []string{"person-child", "person-child-expects-false"}, GroupPropertyKeysKnownAbsent: orgTierAbsent})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, false, snapshot.GetFlag("person-child"))
		require.Equal(t, true, snapshot.GetFlag("person-child-expects-false"))

		// Evaluated directly, the group flag without its group is false too.
		snapshot, remoteCalls = evaluate(t, definitions, EvaluateFlagsPayload{FlagKeys: []string{"org-parent"}, GroupPropertyKeysKnownAbsent: orgTierAbsent, OnlyEvaluateLocally: true})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, false, snapshot.GetFlag("org-parent"))
	})

	t.Run("a group dependency with only conditions on its missing group is false before its prerequisites are evaluated", func(t *testing.T) {
		needsServer := flagDefinition("needs-server", flagCondition(100, staticCohortFilter))
		orgParent := orgFlagDefinition("org-parent", flagCondition(100, flagDependencyFilter("needs-server", true, "needs-server")))
		personChildExpectsFalse := flagDefinition("person-child-expects-false", flagCondition(100, flagDependencyFilter("org-parent", false, "needs-server", "org-parent")))
		snapshot, remoteCalls := evaluate(t, knownAbsentDefinitions(needsServer, orgParent, personChildExpectsFalse), EvaluateFlagsPayload{FlagKeys: []string{"person-child-expects-false"}})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, true, snapshot.GetFlag("person-child-expects-false"))
	})

	t.Run("every reference to a group dependency without its group is false", func(t *testing.T) {
		needsServer := flagDefinition("needs-server", flagCondition(100, staticCohortFilter))
		orgParent := orgFlagDefinition("org-parent", flagCondition(100, flagDependencyFilter("needs-server", true, "needs-server")))
		personChild := flagDefinition("person-child",
			flagCondition(100, flagDependencyFilter("org-parent", true, "needs-server", "org-parent")),
			flagCondition(100, flagDependencyFilter("org-parent", false, "needs-server", "org-parent")))
		snapshot, remoteCalls := evaluate(t, knownAbsentDefinitions(needsServer, orgParent, personChild), EvaluateFlagsPayload{FlagKeys: []string{"person-child"}})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, true, snapshot.GetFlag("person-child"))
	})

	t.Run("a person dependency reads person properties", func(t *testing.T) {
		personParent := flagDefinition("person-parent", flagCondition(100, `{`+regionIsUS+`, "type": "person"}`))
		orgChild := orgFlagDefinition("org-child", flagCondition(100, flagDependencyFilter("person-parent", true, "person-parent")))
		snapshot, remoteCalls := evaluate(t, knownAbsentDefinitions(personParent, orgChild), EvaluateFlagsPayload{
			FlagKeys:         []string{"org-child", "person-parent"},
			Groups:           Groups{"organization": "acme"},
			PersonProperties: Properties{"region": "us"},
			GroupProperties:  map[string]Properties{"organization": {"region": "eu"}},
		})
		require.Zero(t, remoteCalls)
		require.Empty(t, snapshot.UnresolvedFlags())
		require.Equal(t, true, snapshot.GetFlag("person-parent"))
		require.Equal(t, true, snapshot.GetFlag("org-child"))
	})
}

func TestFlagDependencyBucketsByItsOwnIdentifier(t *testing.T) {
	const distinctId = "user-123"
	// Organizations whose hash for the dependency is on the other side of a 50% rollout from the distinct id.
	contrasting := func(t *testing.T, flagKey string) []string {
		t.Helper()
		var orgs []string
		for i := 0; len(orgs) < 5 && i < 200; i++ {
			org := fmt.Sprintf("org-%d", i)
			if checkIfSimpleFlagEnabled(flagKey, org, 50) != checkIfSimpleFlagEnabled(flagKey, distinctId, 50) {
				orgs = append(orgs, org)
			}
		}
		require.Len(t, orgs, 5)
		return orgs
	}

	t.Run("a group dependency hashes the group key", func(t *testing.T) {
		definitions := knownAbsentDefinitions(
			orgFlagDefinition("org-parent", flagCondition(50)),
			flagDefinition("person-child", flagCondition(100, flagDependencyFilter("org-parent", true, "org-parent"))),
		)
		for _, org := range contrasting(t, "org-parent") {
			got, err := computeKnownAbsentFlag(t, definitions, "person-child", Groups{"organization": org}, nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, checkIfSimpleFlagEnabled("org-parent", org, 50), got, org)
		}
	})
	t.Run("a person dependency hashes the distinct id", func(t *testing.T) {
		definitions := knownAbsentDefinitions(
			flagDefinition("person-parent", flagCondition(50)),
			orgFlagDefinition("org-child", flagCondition(100, flagDependencyFilter("person-parent", true, "person-parent"))),
		)
		for _, org := range contrasting(t, "person-parent") {
			got, err := computeKnownAbsentFlag(t, definitions, "org-child", Groups{"organization": org}, nil, map[string]Properties{"organization": {}}, nil)
			require.NoError(t, err)
			require.Equal(t, checkIfSimpleFlagEnabled("person-parent", distinctId, 50), got, org)
		}
	})
}

// A group filter with an explicit group_type_index reads that group type, as the flags
// service routes it in PropertyContext::resolve_for_filter and filter_needs_partial_props.
func TestGroupFilterWithIndexReadsItsOwnGroupType(t *testing.T) {
	projectTier := func(operator, value string) string {
		return `{"key": "tier", "type": "group", "group_type_index": 1, "operator": "` + operator + `", "value": ` + value + `}`
	}
	orgFlag := func(filter string) string {
		return knownAbsentDefinitions(orgFlagDefinition("org-flag", flagCondition(100, filter)))
	}
	bothGroups := Groups{"organization": "acme", "project": "p1"}
	orgOnly := Groups{"organization": "acme"}

	t.Run("the condition's group listing doesn't resolve a filter on another group type", func(t *testing.T) {
		_, err := computeKnownAbsentFlag(t, orgFlag(projectTier("is_not_set", "null")), "org-flag", bothGroups, nil,
			map[string]Properties{"organization": {}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"organization": {"tier"}}))
		requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	})
	t.Run("the filter's group type listing applies", func(t *testing.T) {
		got, err := computeKnownAbsentFlag(t, orgFlag(projectTier("is_not_set", "null")), "org-flag", bothGroups, nil,
			map[string]Properties{"organization": {}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"project": {"tier"}}))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("the filter's group type properties apply", func(t *testing.T) {
		groupProperties := map[string]Properties{"organization": {"tier": "silver"}, "project": {"tier": "gold"}}
		got, err := computeKnownAbsentFlag(t, orgFlag(projectTier("exact", `"gold"`)), "org-flag", bothGroups, nil, groupProperties, nil)
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	t.Run("person condition reads the filter's group type", func(t *testing.T) {
		defs := knownAbsentDefinitions(flagDefinition("person-flag", flagCondition(100, projectTier("exact", `"gold"`))))
		got, err := computeKnownAbsentFlag(t, defs, "person-flag", Groups{"project": "p1"}, Properties{"tier": "silver"},
			map[string]Properties{"project": {"tier": "gold"}}, newKnownAbsentPropertyKeys([]string{"tier"}, nil))
		require.NoError(t, err)
		require.Equal(t, true, got)
	})
	// Without a usable key for the filter's group type, the flags service reads the
	// supplied properties as complete, so a missing key is absent.
	t.Run("no key for the filter's group type", func(t *testing.T) {
		for _, tt := range []struct {
			name            string
			filter          string
			groupProperties map[string]Properties
			want            bool
		}{
			{"no properties, is_not_set", projectTier("is_not_set", "null"), nil, true},
			{"no properties, exact", projectTier("exact", `"gold"`), nil, false},
			{"properties without the key, is_not_set", projectTier("is_not_set", "null"), map[string]Properties{"project": {}}, true},
			{"properties with the key, exact", projectTier("exact", `"gold"`), map[string]Properties{"project": {"tier": "gold"}}, true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				for _, groups := range []Groups{orgOnly, {"organization": "acme", "project": ""}} {
					got, err := computeKnownAbsentFlag(t, orgFlag(tt.filter), "org-flag", groups, nil, tt.groupProperties, nil)
					require.NoError(t, err, "groups %v", groups)
					require.Equal(t, tt.want, got, "groups %v", groups)
				}
			})
		}
		t.Run("listed key overrides a supplied value", func(t *testing.T) {
			got, err := computeKnownAbsentFlag(t, orgFlag(projectTier("is_not_set", "null")), "org-flag", orgOnly, nil,
				map[string]Properties{"project": {"tier": "gold"}}, newKnownAbsentPropertyKeys(nil, map[string][]string{"project": {"tier"}}))
			require.NoError(t, err)
			require.Equal(t, true, got)
		})
		t.Run("an unusable key is still the $group_key", func(t *testing.T) {
			filter := `{"key": "$group_key", "type": "group", "group_type_index": 1, "operator": "is_set"}`
			got, err := computeKnownAbsentFlag(t, orgFlag(filter), "org-flag", Groups{"organization": "acme", "project": ""}, nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, true, got)
		})
	})
	t.Run("index without a group type", func(t *testing.T) {
		unmappedTier := `{"key": "tier", "type": "group", "group_type_index": 5, "operator": "is_not_set"}`
		t.Run("with group context the filter doesn't match", func(t *testing.T) {
			got, err := computeKnownAbsentFlag(t, orgFlag(unmappedTier), "org-flag", orgOnly, nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, false, got)

			defs := knownAbsentDefinitions(flagDefinition("person-flag", flagCondition(100, unmappedTier)))
			got, err = computeKnownAbsentFlag(t, defs, "person-flag", nil, Properties{"tier": "gold"}, map[string]Properties{"other": {"plan": "pro"}}, nil)
			require.NoError(t, err)
			require.Equal(t, false, got)
		})
		t.Run("without group context the filter reads no properties", func(t *testing.T) {
			defs := knownAbsentDefinitions(flagDefinition("person-flag", flagCondition(100, unmappedTier)))
			for _, groupProperties := range []map[string]Properties{nil, {"organization": {"$group_key": ""}}} {
				got, err := computeKnownAbsentFlag(t, defs, "person-flag", Groups{"organization": ""}, Properties{"tier": "gold"}, groupProperties, nil)
				require.NoError(t, err)
				require.Equal(t, true, got)
			}
		})
	})
	t.Run("the client falls back to /flags", func(t *testing.T) {
		server := newKnownAbsentServer(t, knownAbsentDefinitions(`{"key": "profile-incomplete", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [`+
			flagCondition(100, projectTier("is_not_set", "null"))+`]}}`))
		c, _, _ := newEvalClient(t, server.server, func(cfg *Config) { cfg.PersonalApiKey = "personal-key" })
		waitForFlagDefinitions(t, c)
		got, err := c.GetFeatureFlag(FeatureFlagPayload{
			Key: "profile-incomplete", DistinctId: "user-1", Groups: bothGroups,
			GroupProperties:              map[string]Properties{"organization": {}},
			GroupPropertyKeysKnownAbsent: map[string][]string{"organization": {"tier"}},
		})
		require.NoError(t, err)
		require.Equal(t, true, got, "the /flags result")
		require.Len(t, server.requests(), 1, "falls back to /flags")
	})
}
