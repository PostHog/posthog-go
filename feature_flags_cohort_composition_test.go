package posthog

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnsupportedCohortChildComposition(t *testing.T) {
	unsupported := map[string]any{"type": "XOR", "values": []any{}}
	cohorts := map[string]PropertyGroup{"invalid": {Type: "XOR"}}
	poller := &FeatureFlagsPoller{}
	for _, tc := range []struct {
		groupType  string
		match      bool
		want       bool
		unresolved bool
	}{
		{groupType: "OR", match: true, want: true},
		{groupType: "AND", match: false},
		{groupType: "OR", match: false, unresolved: true},
		{groupType: "AND", match: true, unresolved: true},
	} {
		for _, child := range []struct {
			name  string
			value any
		}{
			{name: "nested group", value: unsupported},
			{name: "negated cohort reference", value: map[string]any{"type": "cohort", "value": "invalid", "negation": true}},
		} {
			for _, invalidFirst := range []bool{true, false} {
				for _, parsed := range []bool{false, true} {
					name := fmt.Sprintf("%s/match=%t/%s/invalidFirst=%t/parsed=%t", tc.groupType, tc.match, child.name, invalidFirst, parsed)
					t.Run(name, func(t *testing.T) {
						value := "other@x"
						if tc.match {
							value = "user@x"
						}
						sibling := map[string]any{"key": "email", "operator": "exact", "value": value}
						values := []any{child.value, sibling}
						if !invalidFirst {
							values[0], values[1] = values[1], values[0]
						}
						group := PropertyGroup{Type: tc.groupType, Values: values}
						localCohorts := cohorts
						if parsed {
							group = preParsePG(group)
							localCohorts = preParseCohortValues(cohorts)
						}
						got, err := poller.matchPropertyGroup(group, Properties{"email": "user@x"}, localCohorts, nil, nil, "user", nil, nil)
						if tc.unresolved {
							requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
						} else {
							require.NoError(t, err)
						}
						require.Equal(t, tc.want, got)
					})
				}
			}
		}
	}
}

func TestDecisiveCohortSiblingStaysLocal(t *testing.T) {
	for _, tc := range []struct {
		groupType, leafValue string
		want                 bool
	}{
		{groupType: "OR", leafValue: "user@x", want: true},
		{groupType: "AND", leafValue: "other@x"},
	} {
		for _, invalidFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/invalidFirst=%t", tc.groupType, invalidFirst), func(t *testing.T) {
				invalid := `{"type":"XOR","values":[{"type":"person","key":"email","operator":"exact","value":"other@x"}]}`
				decisive := `{"type":"AND","values":[{"type":"person","key":"email","operator":"exact","value":"` + tc.leafValue + `"}]}`
				values := invalid + `,` + decisive
				if !invalidFirst {
					values = decisive + `,` + invalid
				}
				cohort := `{"type":"` + tc.groupType + `","values":[` + values + `]}`
				definitions := localDefinitionsWithCohorts(`{"1":`+cohort+`}`,
					flagDefinition("checkout", flagCondition(100, `{"type":"cohort","value":1}`)))
				client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, remoteCheckoutEnabled)
				payload := FeatureFlagPayload{Key: "checkout", DistinctId: "user", PersonProperties: Properties{"email": "user@x"}, OnlyEvaluateLocally: true}
				got, err := client.GetFeatureFlag(payload)
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				require.Zero(t, remoteCalls.Load())

				payload.OnlyEvaluateLocally = false
				got, err = client.GetFeatureFlag(payload)
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				require.Zero(t, remoteCalls.Load())
			})
		}
	}
}
