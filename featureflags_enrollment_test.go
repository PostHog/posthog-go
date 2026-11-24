package posthog

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Early-access features: the person's opt-in in `$feature_enrollment/<flag key>` decides the
// flag before holdouts and release conditions (sdk-specs local-feature-flag-evaluator,
// acceptance/private/local-feature-flag-enrollment.feature).

const enrollmentKey = "$feature_enrollment/beta-feature"

func enrollmentFlagDefinition(filters string) string {
	return fmt.Sprintf(`{"id": 1, "key": "beta-feature", "active": true, "filters": {"feature_enrollment": true, %s}}`, filters)
}

const (
	everyoneOn  = `"groups": [{"properties": [], "rollout_percentage": 100}]`
	everyoneOff = `"groups": [{"properties": [], "rollout_percentage": 0}]`
)

func dependentOnBetaFeature() string {
	return `{"id": 2, "key": "dependent", "active": true, "filters": {"groups": [{"properties": [` +
		flagDependencyFilter("beta-feature", true, "beta-feature") +
		`], "rollout_percentage": 100}]}}`
}

func TestFeatureEnrollmentValues(t *testing.T) {
	type namedBool bool
	type namedString string
	enrolled, optedOut := true, false
	for _, tt := range []struct {
		name       string
		enrollment interface{}
		groups     string
		want       interface{}
	}{
		{"enrolled boolean", true, everyoneOff, true},
		{"enrolled string", "true", everyoneOff, true},
		{"opted out boolean", false, everyoneOn, false},
		{"opted out string", "false", everyoneOn, false},
		{"numeric one", float64(1), everyoneOn, false},
		{"numeric zero", float64(0), everyoneOn, false},
		{"explicit null", nil, everyoneOn, false},
		{"uppercase string", "True", everyoneOn, false},
		{"empty string", "", everyoneOn, false},
		{"empty array", []interface{}{}, everyoneOn, false},
		{"empty object", map[string]interface{}{}, everyoneOn, false},
		{"pointer to true", &enrolled, everyoneOff, true},
		{"pointer to false", &optedOut, everyoneOn, false},
		{"nil pointer", (*bool)(nil), everyoneOn, false},
		{"named boolean", namedBool(true), everyoneOff, true},
		{"named string", namedString("true"), everyoneOff, true},
		{"raw JSON", json.RawMessage(`true`), everyoneOff, true},
		{"array with true", []bool{true}, everyoneOn, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeDefinedFlag(t, localDefinitions(enrollmentFlagDefinition(tt.groups)), "beta-feature",
				Properties{enrollmentKey: tt.enrollment})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFeatureEnrollmentPrecedesHoldout(t *testing.T) {
	holdout := `"holdout": {"id": 727, "exclusion_percentage": 100}, `
	got, err := computeDefinedFlag(t, localDefinitions(enrollmentFlagDefinition(holdout+everyoneOff)), "beta-feature",
		Properties{enrollmentKey: true})
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = computeDefinedFlag(t, localDefinitions(enrollmentFlagDefinition(holdout+everyoneOn)), "beta-feature",
		Properties{enrollmentKey: false})
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestFeatureEnrollmentKeepsInactiveFlagOff(t *testing.T) {
	inactive := `{"id": 1, "key": "beta-feature", "active": false, "filters": {"feature_enrollment": true, ` + everyoneOn + `}}`
	for name, properties := range map[string]Properties{
		"enrolled":           {enrollmentKey: true},
		"without enrollment": {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := computeDefinedFlag(t, localDefinitions(inactive), "beta-feature", properties)
			require.NoError(t, err)
			require.Equal(t, false, got)
		})
	}
}

func TestFeatureEnrollmentMarkerOffUsesOrdinaryTargeting(t *testing.T) {
	for _, marker := range []string{"false", "null"} {
		t.Run(marker, func(t *testing.T) {
			flag := `{"id": 1, "key": "beta-feature", "active": true, "filters": {"feature_enrollment": ` + marker + `, ` + everyoneOn + `}}`
			got, err := computeDefinedFlag(t, localDefinitions(flag), "beta-feature", Properties{})
			require.NoError(t, err)
			require.Equal(t, true, got)
		})
	}
}

func TestFeatureEnrollmentMissingIsInconclusive(t *testing.T) {
	_, err := computeDefinedFlag(t, localDefinitions(enrollmentFlagDefinition(everyoneOn)), "beta-feature", Properties{})
	requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)
	require.False(t, isServerEvalError(err))
}

func TestFeatureEnrollmentContinuityWins(t *testing.T) {
	flag := `{"id": 1, "key": "beta-feature", "active": true, "ensure_experience_continuity": true, "filters": {"feature_enrollment": true, ` + everyoneOn + `}}`
	for name, properties := range map[string]Properties{
		"enrolled":           {enrollmentKey: true},
		"opted out":          {enrollmentKey: false},
		"without enrollment": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := computeDefinedFlag(t, localDefinitions(flag), "beta-feature", properties)
			requireUnresolvedReason(t, err, UnresolvedFlagReasonExperienceContinuity)
		})
	}
}

func TestFeatureEnrollmentSkipsUnsupportedReleaseConditions(t *testing.T) {
	// A static cohort that is not in the definitions: unsupported, but never read.
	definitions := localDefinitions(enrollmentFlagDefinition(
		`"groups": [{"properties": [{"key": "id", "type": "cohort", "value": 123}], "rollout_percentage": 100}]`))

	got, err := computeDefinedFlag(t, definitions, "beta-feature", Properties{enrollmentKey: true})
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = computeDefinedFlag(t, definitions, "beta-feature", Properties{enrollmentKey: false})
	require.NoError(t, err)
	require.Equal(t, false, got)

	_, err = computeDefinedFlag(t, definitions, "beta-feature", Properties{})
	requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)

	poller := newDefinitionsPoller(t, definitions)
	state := poller.state.Load()
	require.Equal(t, UnresolvedFlagReason(""),
		definitionUnresolvedReason(state.flagsByKey["beta-feature"], state.flagsByKey, state.cohorts, state.propertyMatchingVersion))
}

func TestFeatureEnrollmentOnDependentFlag(t *testing.T) {
	for _, tt := range []struct {
		enrolled bool
		groups   string
	}{
		{true, everyoneOff},
		{false, everyoneOn},
	} {
		t.Run(fmt.Sprint(tt.enrolled), func(t *testing.T) {
			definitions := localDefinitions(enrollmentFlagDefinition(tt.groups), dependentOnBetaFeature())
			got, err := computeDefinedFlag(t, definitions, "dependent", Properties{enrollmentKey: tt.enrolled})
			require.NoError(t, err)
			require.Equal(t, tt.enrolled, got)
		})
	}

	_, err := computeDefinedFlag(t, localDefinitions(enrollmentFlagDefinition(everyoneOn), dependentOnBetaFeature()), "dependent", Properties{})
	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnresolvedDependency)
}

func TestFeatureEnrollmentOnGroupFlagReadsPersonProperties(t *testing.T) {
	definitions := `{"flags": [` + enrollmentFlagDefinition(`"aggregation_group_type_index": 0, `+everyoneOff) +
		`], "group_type_mapping": {"0": "company"}, "cohorts": {}}`
	poller := newDefinitionsPoller(t, definitions)
	state := poller.state.Load()
	flag := state.flagsByKey["beta-feature"]

	// No group context: the person's enrollment still decides.
	got, err := poller.computeFlagLocally(flag, "user-123", nil, Groups{}, Properties{enrollmentKey: true}, nil, state.cohorts, state)
	require.NoError(t, err)
	require.Equal(t, true, got)

	// Group properties can't turn the person's opt-out into enrollment.
	got, err = poller.computeFlagLocally(flag, "user-123", nil, Groups{"company": "acme"}, Properties{enrollmentKey: false},
		map[string]Properties{"company": {enrollmentKey: true}}, state.cohorts, state)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// A dependency reuses its caller's properties, and a group-aggregated caller has the group's.
func TestFeatureEnrollmentDependencyOfGroupFlagNeedsServer(t *testing.T) {
	dependency := flagDependencyFilter("beta-feature", true, "beta-feature")
	for name, aggregation := range map[string]string{
		"same group":  `"aggregation_group_type_index": 0, `,
		"person flag": ``,
	} {
		t.Run(name, func(t *testing.T) {
			definitions := `{"group_type_mapping": {"0": "company"}, "cohorts": {}, "flags": [` +
				enrollmentFlagDefinition(aggregation+everyoneOff) + `,
				{"id": 2, "key": "target-multi", "active": true, "filters": {
					"aggregation_group_type_index": 0,
					"multivariate": {"variants": [{"key": "enrolled", "rollout_percentage": 50}, {"key": "fallback", "rollout_percentage": 50}]},
					"groups": [
						{"properties": [` + dependency + `], "rollout_percentage": 100, "variant": "enrolled"},
						{"properties": [], "rollout_percentage": 100, "variant": "fallback"}
					]}},
				{"id": 3, "key": "target-single", "active": true, "filters": {
					"aggregation_group_type_index": 0,
					"groups": [{"properties": [` + dependency + `], "rollout_percentage": 100}]}}
			]}`
			client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions,
				`{"flags": {"target-multi": {"key": "target-multi", "enabled": true, "variant": "enrolled", "metadata": {"id": 2, "version": 1}}}}`)
			enrolled := Properties{enrollmentKey: true}
			groupProperties := map[string]Properties{"company": {enrollmentKey: false}}

			for _, groups := range []Groups{{"company": "acme"}, nil} {
				snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{
					DistinctId:          "user-123",
					Groups:              groups,
					PersonProperties:    enrolled,
					GroupProperties:     groupProperties,
					OnlyEvaluateLocally: true,
				})
				require.NoError(t, err)
				require.True(t, snapshot.IsEnabled("beta-feature"))
				require.Equal(t, UnresolvedFlagReasonUnsupportedDefinition, snapshot.UnresolvedFlags()["target-single"])
				require.Equal(t, UnresolvedFlagReasonUnsupportedDefinition, snapshot.UnresolvedFlags()["target-multi"])
			}
			require.Zero(t, remoteCalls.Load())

			// The second condition must not answer instead of /flags.
			got, err := client.GetFeatureFlag(FeatureFlagPayload{
				Key:              "target-multi",
				DistinctId:       "user-123",
				Groups:           Groups{"company": "acme"},
				PersonProperties: enrolled,
				GroupProperties:  groupProperties,
			})
			require.NoError(t, err)
			require.Equal(t, "enrolled", got)
			require.Equal(t, int32(1), remoteCalls.Load())
		})
	}
}

// Enrollment skips the dependency's own aggregation, so a person flag can read it locally.
func TestFeatureEnrollmentDependencyIgnoresItsOwnAggregation(t *testing.T) {
	for name, filters := range map[string]string{
		"group condition": `"groups": [{"aggregation_group_type_index": 0, "properties": [], "rollout_percentage": 0}]`,
		"group flag":      `"aggregation_group_type_index": 0, ` + everyoneOff,
	} {
		t.Run(name, func(t *testing.T) {
			definitions := `{"flags": [` + enrollmentFlagDefinition(filters) + `,` + dependentOnBetaFeature() +
				`], "group_type_mapping": {"0": "company"}, "cohorts": {}}`
			client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, "")

			for _, enrollment := range []bool{true, false} {
				snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{
					DistinctId:          "user-123",
					PersonProperties:    Properties{enrollmentKey: enrollment},
					OnlyEvaluateLocally: true,
				})
				require.NoError(t, err)
				require.Equal(t, enrollment, snapshot.IsEnabled("beta-feature"))
				require.Equal(t, enrollment, snapshot.IsEnabled("dependent"))
				require.Empty(t, snapshot.UnresolvedFlags())
			}
			require.Zero(t, remoteCalls.Load())
		})
	}
}

func TestFeatureEnrollmentMissingFallsBackToFlags(t *testing.T) {
	client, _, remoteCalls := newUnresolvedFlagsClient(t, localDefinitions(enrollmentFlagDefinition(everyoneOn)),
		`{"flags": {"beta-feature": {"key": "beta-feature", "enabled": false, "metadata": {"id": 1, "version": 1}}}}`)

	got, err := client.GetFeatureFlag(FeatureFlagPayload{Key: "beta-feature", DistinctId: "user-123"})

	require.NoError(t, err)
	require.Equal(t, false, got)
	require.Equal(t, int32(1), remoteCalls.Load())
}

func TestFeatureEnrollmentResolvesPayloadsLocally(t *testing.T) {
	client, _, remoteCalls := newUnresolvedFlagsClient(t, localDefinitions(enrollmentFlagDefinition(
		`"multivariate": {"variants": [{"key": "test", "rollout_percentage": 100}]}, "payloads": {"true": "{\"color\": \"blue\"}"}, `+everyoneOff)), "")

	for _, tt := range []struct {
		enrolled bool
		payload  string
	}{
		{true, `{"color": "blue"}`},
		{false, ""},
	} {
		result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
			Key:              "beta-feature",
			DistinctId:       "user-123",
			PersonProperties: Properties{enrollmentKey: tt.enrolled},
		})
		require.NoError(t, err)
		require.Equal(t, tt.enrolled, result.Enabled)
		require.Nil(t, result.Variant)
		if tt.payload == "" {
			require.Nil(t, result.RawPayload)
		} else {
			require.JSONEq(t, tt.payload, *result.RawPayload)
		}
	}
	require.Zero(t, remoteCalls.Load())
}

func TestFeatureEnrollmentMissingLeavesOnlyDependentsUnresolved(t *testing.T) {
	client, _, remoteCalls := newUnresolvedFlagsClient(t,
		localDefinitions(enrollmentFlagDefinition(everyoneOn), dependentOnBetaFeature(), betaUIDefinition), "")

	snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", OnlyEvaluateLocally: true})

	require.NoError(t, err)
	require.True(t, snapshot.IsEnabled("beta-ui"))
	require.Equal(t, map[string]UnresolvedFlagReason{
		"beta-feature": UnresolvedFlagReasonMissingContext,
		"dependent":    UnresolvedFlagReasonUnresolvedDependency,
	}, snapshot.UnresolvedFlags())
	require.Zero(t, remoteCalls.Load())
}

// Group properties don't stand in for a missing person enrollment.
func TestFeatureEnrollmentMissingOnGroupFlag(t *testing.T) {
	definitions := `{"flags": [` + enrollmentFlagDefinition(`"aggregation_group_type_index": 0, `+everyoneOn) +
		`], "group_type_mapping": {"0": "company"}, "cohorts": {}}`
	client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, "")

	snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{
		DistinctId:          "user-123",
		Groups:              Groups{"company": "acme"},
		GroupProperties:     map[string]Properties{"company": {enrollmentKey: true}},
		OnlyEvaluateLocally: true,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]UnresolvedFlagReason{"beta-feature": UnresolvedFlagReasonMissingContext}, snapshot.UnresolvedFlags())
	require.Zero(t, remoteCalls.Load())
}
