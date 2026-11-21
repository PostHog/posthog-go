package posthog

import (
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
