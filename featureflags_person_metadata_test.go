package posthog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A cohort can filter on Person metadata > First seen (type "person_metadata", key
// "created_at"). /flags reads that value from the persons table and ignores a created_at
// person property, so local evaluation must not match it against one.
// See https://github.com/PostHog/posthog/issues/137503.

const firstSeenFilter = `{"key": "created_at", "type": "person_metadata", "operator": "is_date_after", "value": "2025-01-01"}`

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
