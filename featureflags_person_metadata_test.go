package posthog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cohort can filter on Person metadata > First seen (type "person_metadata", key
// "created_at"). /flags reads that value from the persons table and ignores a created_at
// person property, so local evaluation must not match it against one.
// See https://github.com/PostHog/posthog/issues/137503.

const firstSeenFilter = `{"key": "created_at", "type": "person_metadata", "operator": "is_date_after", "value": "2025-01-01"}`

const planFilter = `{"key": "plan", "type": "person", "operator": "exact", "value": ["paid"]}`

func TestPersonMetadataFilterInCohortIsUnsupported(t *testing.T) {
	definitions := localDefinitionsWithCohorts(
		`{"7": {"type": "AND", "values": [`+firstSeenFilter+`]}}`,
		flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 7}`)),
	)

	_, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"created_at": "2026-06-01"})

	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
}

func TestPersonMetadataFilterInConditionIsUnsupported(t *testing.T) {
	definitions := localDefinitions(flagDefinition("first-seen-flag", flagCondition(100, firstSeenFilter)))

	_, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"created_at": "2026-06-01"})

	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
}

func TestPersonMetadataFilterMakesTheDefinitionUnsupported(t *testing.T) {
	for name, definitions := range map[string]string{
		"in a cohort": localDefinitionsWithCohorts(
			`{"7": {"type": "AND", "values": [`+firstSeenFilter+`]}}`,
			flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 7}`)),
		),
		"in a condition": localDefinitions(flagDefinition("first-seen-flag", flagCondition(100, firstSeenFilter))),
	} {
		t.Run(name, func(t *testing.T) {
			poller := newDefinitionsPoller(t, definitions)
			state := poller.state.Load()
			require.Equal(t, UnresolvedFlagReasonUnsupportedDefinition,
				definitionUnresolvedReason(state.flagsByKey["first-seen-flag"], state.flagsByKey, state.cohorts, state.propertyMatchingVersion))
		})
	}
}

func TestPersonFilterOnCreatedAtIsStillLocal(t *testing.T) {
	definitions := localDefinitions(flagDefinition("first-seen-flag", flagCondition(100,
		`{"key": "created_at", "type": "person", "operator": "is_date_after", "value": "2025-01-01"}`)))

	got, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"created_at": "2026-06-01"})

	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestPersonMetadataFilterFallsBackToFlags(t *testing.T) {
	definitions := localDefinitionsWithCohorts(
		`{"7": {"type": "AND", "values": [`+firstSeenFilter+`]}}`,
		flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 7}`)),
	)
	// The person was created in 2020, so /flags answers false.
	client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions,
		`{"flags": {"first-seen-flag": {"key": "first-seen-flag", "enabled": false, "metadata": {"id": 1, "version": 1}}}}`)

	value, err := client.GetFeatureFlag(FeatureFlagPayload{
		Key:              "first-seen-flag",
		DistinctId:       "user-123",
		PersonProperties: Properties{"created_at": "2026-06-01"},
	})

	require.NoError(t, err)
	require.Equal(t, false, value)
	require.Equal(t, int32(1), remoteCalls.Load())
}

// A value /flags alone can read must not let a later condition decide the flag.
func TestPersonMetadataMultiConditionRequiresServerFallback(t *testing.T) {
	const cohorts = `{"7": {"type": "AND", "values": [` + firstSeenFilter + `]}}`
	const cohortFilter = `{"key": "id", "type": "cohort", "value": 7}`
	cases := []struct {
		name           string
		definitions    string
		flagKey        string
		remoteResponse string
		want           interface{}
	}{
		{
			name: "multivariate_ordered_variants",
			definitions: localDefinitionsWithCohorts(cohorts, `{"key": "first-seen-mv", "active": true, "filters": {
				"multivariate": {"variants": [{"key": "new-user", "rollout_percentage": 50}, {"key": "existing-user", "rollout_percentage": 50}]},
				"groups": [
					{"properties": [`+cohortFilter+`], "rollout_percentage": 100, "variant": "new-user"},
					{"properties": [], "rollout_percentage": 100, "variant": "existing-user"}
				]}}`),
			flagKey:        "first-seen-mv",
			remoteResponse: `{"flags": {"first-seen-mv": {"key": "first-seen-mv", "enabled": true, "variant": "new-user", "metadata": {"id": 1, "version": 1}}}}`,
			want:           "new-user",
		},
		{
			name: "early_exit_exclusion",
			definitions: localDefinitionsWithCohorts(cohorts, `{"key": "first-seen-early-exit", "active": true, "filters": {
				"early_exit": true,
				"groups": [
					{"properties": [`+cohortFilter+`], "rollout_percentage": 0},
					{"properties": [], "rollout_percentage": 100}
				]}}`),
			flagKey:        "first-seen-early-exit",
			remoteResponse: `{"flags": {"first-seen-early-exit": {"key": "first-seen-early-exit", "enabled": false, "metadata": {"id": 2, "version": 1}}}}`,
			want:           false,
		},
		{
			name: "flag_dependency",
			definitions: localDefinitionsWithCohorts(cohorts,
				flagDefinition("dep-first-seen", flagCondition(100, cohortFilter)),
				`{"key": "target-flag", "active": true, "filters": {
					"multivariate": {"variants": [{"key": "a", "rollout_percentage": 50}, {"key": "b", "rollout_percentage": 50}]},
					"groups": [
						{"properties": [{"key": "dep-first-seen", "type": "flag", "value": true, "operator": "flag_evaluates_to", "dependency_chain": ["dep-first-seen"]}], "rollout_percentage": 100, "variant": "a"},
						{"properties": [], "rollout_percentage": 100, "variant": "b"}
					]}}`),
			flagKey:        "target-flag",
			remoteResponse: `{"flags": {"target-flag": {"key": "target-flag", "enabled": true, "variant": "a", "metadata": {"id": 3, "version": 1}}}}`,
			want:           "a",
		},
	}
	for _, tc := range cases {
		for _, propCase := range []struct {
			name  string
			props Properties
		}{
			{"with_created_at", Properties{"created_at": "2026-06-01"}},
			{"without_created_at", nil},
		} {
			t.Run(tc.name+"/"+propCase.name, func(t *testing.T) {
				client, _, remoteCalls := newUnresolvedFlagsClient(t, tc.definitions, tc.remoteResponse)
				got, err := client.GetFeatureFlag(FeatureFlagPayload{
					Key:              tc.flagKey,
					DistinctId:       "user-123",
					PersonProperties: propCase.props,
				})
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				require.Equal(t, int32(1), remoteCalls.Load())
			})
		}
	}
}

func TestPersonMetadataFilterWithoutTheValueIsUnsupported(t *testing.T) {
	definitions := localDefinitions(flagDefinition("first-seen-flag", flagCondition(100, firstSeenFilter)))

	_, err := computeDefinedFlag(t, definitions, "first-seen-flag", nil)

	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
}

func TestPersonMetadataFilterIsNotInverted(t *testing.T) {
	negatedFilter := strings.Replace(firstSeenFilter, "}", `, "negation": true}`, 1)
	for name, definitions := range map[string]string{
		"negated cohort leaf": localDefinitionsWithCohorts(
			`{"7": {"type": "AND", "values": [`+negatedFilter+`]}}`,
			flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "value": 7}`)),
		),
		"not_in cohort": localDefinitionsWithCohorts(
			`{"7": {"type": "AND", "values": [`+firstSeenFilter+`]}}`,
			flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "operator": "not_in", "value": 7}`)),
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"created_at": "2026-06-01"})
			requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
		})
	}
}

// Another filter that does not match still rules the condition out locally.
func TestPersonMetadataFilterDoesNotOutrankAMismatch(t *testing.T) {
	definitions := localDefinitions(flagDefinition("first-seen-flag", flagCondition(100, firstSeenFilter, planFilter)))

	got, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"plan": "free"})

	require.NoError(t, err)
	require.Equal(t, false, got)
}

// Cohorts use three-valued logic, so another filter can still make the result certain.
func TestPersonMetadataFilterDoesNotHideADecidedCohort(t *testing.T) {
	dependency := `{"key": "dependency", "type": "flag", "value": true, "operator": "flag_evaluates_to", "dependency_chain": ["dependency"]}`
	nested := `{"type": "AND", "values": [` + firstSeenFilter + `]}`
	// The unknown operator's reason outranks the dependency's, which replaces the error.
	nestedReasons := `{"type": "AND", "values": [{"key": "age", "type": "person", "operator": "unknown", "value": 1},` + dependency + `]}`
	for _, tc := range []struct {
		name      string
		groupType string
		operator  string
		plan      string
		want      interface{}
	}{
		{"AND", "AND", "in", "free", false},
		{"AND not_in", "AND", "not_in", "free", true},
		{"OR", "OR", "in", "paid", true},
		{"OR not_in", "OR", "not_in", "paid", false},
	} {
		for _, values := range []string{
			planFilter + "," + firstSeenFilter,
			firstSeenFilter + "," + planFilter,
			nested + "," + planFilter,
			dependency + "," + planFilter,
			nestedReasons + "," + planFilter,
		} {
			definitions := localDefinitionsWithCohorts(
				`{"7": {"type": "`+tc.groupType+`", "values": [`+values+`]}}`,
				flagDefinition("first-seen-flag", flagCondition(100, `{"key": "id", "type": "cohort", "operator": "`+tc.operator+`", "value": 7}`)),
				flagDefinition("dependency", flagCondition(100, firstSeenFilter)),
			)
			for name, properties := range map[string]Properties{
				"with created_at":    {"plan": tc.plan, "created_at": "2026-06-01"},
				"without created_at": {"plan": tc.plan},
			} {
				t.Run(tc.name+"/"+name+"/"+values, func(t *testing.T) {
					got, err := computeDefinedFlag(t, definitions, "first-seen-flag", properties)
					require.NoError(t, err)
					require.Equal(t, tc.want, got)
				})
			}
		}
	}
}

func TestPersonMetadataFilterInAnUndecidedCohortNeedsServer(t *testing.T) {
	for _, tc := range []struct{ groupType, plan string }{{"AND", "paid"}, {"OR", "free"}} {
		for _, values := range []string{planFilter + "," + firstSeenFilter, firstSeenFilter + "," + planFilter} {
			t.Run(tc.groupType+"/"+values, func(t *testing.T) {
				definitions := localDefinitionsWithCohorts(
					`{"7": {"type": "`+tc.groupType+`", "values": [`+values+`]}}`,
					flagDefinition("first-seen-flag", flagCondition(0, `{"key": "id", "type": "cohort", "value": 7}`), flagCondition(100)),
				)
				_, err := computeDefinedFlag(t, definitions, "first-seen-flag", Properties{"plan": tc.plan})
				requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
				require.True(t, isServerEvalError(err))
			})
		}
	}
}

// The reason follows the spec's precedence, whatever the order of the filters.
func TestPersonMetadataFilterReasonDoesNotDependOnOrder(t *testing.T) {
	dependency := `{"key": "dependency", "type": "flag", "value": true, "operator": "flag_evaluates_to", "dependency_chain": ["dependency"]}`
	group := func(filter string) string { return `{"type": "AND", "values": [` + filter + `]}` }
	flagsByKey := map[string]FeatureFlag{}
	var flag FeatureFlag
	require.NoError(t, json.Unmarshal([]byte(flagDefinition("dependency", flagCondition(100, firstSeenFilter))), &flag))
	flagsByKey["dependency"] = flag

	for _, values := range []string{
		firstSeenFilter + "," + dependency,
		dependency + "," + firstSeenFilter,
		group(firstSeenFilter) + "," + group(dependency),
		group(dependency) + "," + group(firstSeenFilter),
	} {
		t.Run(values, func(t *testing.T) {
			var cohort PropertyGroup
			require.NoError(t, json.Unmarshal([]byte(group(values)), &cohort))

			_, err := (&FeatureFlagsPoller{}).matchPropertyGroup(cohort, Properties{}, nil, flagsByKey, map[string]interface{}{}, "user-123", nil, nil, &flagsState{})

			requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
			require.True(t, isServerEvalError(err))
		})
	}
}

func TestPersonMetadataFilterInEvaluateFlags(t *testing.T) {
	definitions := localDefinitions(flagDefinition("first-seen-flag", flagCondition(100, firstSeenFilter)))
	client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, `{}`)

	snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", OnlyEvaluateLocally: true})

	require.NoError(t, err)
	require.Nil(t, snapshot.GetFlag("first-seen-flag"))
	require.Equal(t, UnresolvedFlagReasonUnsupportedDefinition, snapshot.UnresolvedFlags()["first-seen-flag"])
	require.Zero(t, remoteCalls.Load())
}
