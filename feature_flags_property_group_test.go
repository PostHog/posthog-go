package posthog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchPropertyGroupRawAndParsedParity(t *testing.T) {
	tests := []struct {
		name       string
		group      PropertyGroup
		properties Properties
		want       bool
		wantErr    error
	}{
		{
			name: "nested group matches",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
				map[string]any{"type": "OR", "values": []any{
					map[string]any{"key": "plan", "operator": "exact", "value": "pro"},
					map[string]any{"key": "beta", "operator": "exact", "value": true},
				}},
			}},
			properties: NewProperties().Set("country", "US").Set("plan", "free").Set("beta", true),
			want:       true,
		},
		{
			name: "negated property matches",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "country", "operator": "exact", "value": "CA", "negation": true},
			}},
			properties: NewProperties().Set("country", "US"),
			want:       true,
		},
		{
			name: "inconclusive result is returned after all properties",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value", "negation": true},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "US"),
			wantErr:    errCohortPropertyValue.withReason(UnresolvedFlagReasonMissingContext),
		},
		{
			name: "OR match overrides an inconclusive result",
			group: PropertyGroup{Type: "OR", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value"},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "US"),
			want:       true,
		},
		{
			name: "AND mismatch overrides an inconclusive result",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value", "negation": true},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "CA"),
		},
		{
			name: "negation does not turn an inconclusive leaf into an OR match",
			group: PropertyGroup{Type: "OR", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value", "negation": true},
			}},
			properties: NewProperties().Set("country", "US"),
			wantErr:    errCohortPropertyValue.withReason(UnresolvedFlagReasonMissingContext),
		},
		{
			name: "inconclusive leaf is not an AND mismatch",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value"},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "US"),
			wantErr:    errCohortPropertyValue.withReason(UnresolvedFlagReasonMissingContext),
		},
		{
			name: "inconclusive nested group is not an AND mismatch",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"type": "OR", "values": []any{
					map[string]any{"key": "missing", "operator": "exact", "value": "value"},
				}},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "US"),
			wantErr:    errCohortPropertyValue.withReason(UnresolvedFlagReasonMissingContext),
		},
		{
			name: "AND mismatch after an inconclusive leaf is still a mismatch",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value"},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: NewProperties().Set("country", "CA"),
		},
		{
			name: "inconclusive result keeps the highest reason",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"key": "missing", "operator": "exact", "value": "value"},
				map[string]any{"key": "country", "operator": "is_on_the_moon", "value": "US"},
			}},
			properties: NewProperties().Set("country", "US"),
			wantErr:    errCohortPropertyValue.withReason(UnresolvedFlagReasonUnsupportedDefinition),
		},
		{
			name: "missing cohort requires server evaluation",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"type": "cohort", "value": "missing-cohort"},
			}},
			wantErr: errCohortRequiresServerEval,
		},
		{
			name: "missing cohort before an OR match still requires the server",
			group: PropertyGroup{Type: "OR", Values: []any{
				map[string]any{"type": "cohort", "value": "missing-cohort"},
				map[string]any{"key": "country", "operator": "exact", "value": "US"},
			}},
			properties: Properties{"country": "US"},
			wantErr:    errCohortRequiresServerEval,
		},
		{
			name: "missing cohort before an AND mismatch still requires the server",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"type": "cohort", "value": "missing-cohort"},
				map[string]any{"key": "country", "operator": "exact", "value": "CA"},
			}},
			properties: Properties{"country": "US"},
			wantErr:    errCohortRequiresServerEval,
		},
	}

	poller := &FeatureFlagsPoller{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawResult, rawErr := poller.matchPropertyGroup(tt.group, tt.properties, nil, nil, nil, "distinct-id", nil, nil)
			require.Equal(t, tt.want, rawResult)
			require.Equal(t, tt.wantErr, rawErr)

			parsedGroup := preParsePG(tt.group)
			require.NotEmpty(t, parsedGroup.ParsedValues)
			parsedResult, parsedErr := poller.matchPropertyGroup(parsedGroup, tt.properties, nil, nil, nil, "distinct-id", nil, nil)
			require.Equal(t, rawResult, parsedResult)
			require.Equal(t, rawErr, parsedErr)
		})
	}
}

func TestMatchPropertyGroupTypes(t *testing.T) {
	match := map[string]any{"type": "person", "key": "email", "operator": "exact", "value": "user@x"}
	mismatch := map[string]any{"type": "person", "key": "email", "operator": "exact", "value": "other@x"}
	tests := []struct {
		name           string
		group          PropertyGroup
		want           bool
		wantUnresolved bool
	}{
		{name: "unknown matching", group: PropertyGroup{Type: "XOR", Values: []any{match}}, wantUnresolved: true},
		{name: "unknown mismatching", group: PropertyGroup{Type: "XOR", Values: []any{mismatch}}, wantUnresolved: true},
		{name: "unknown nil values", group: PropertyGroup{Type: "XOR"}, wantUnresolved: true},
		{name: "unknown empty values", group: PropertyGroup{Type: "XOR", Values: []any{}}, wantUnresolved: true},
		{name: "nested unknown", group: PropertyGroup{Type: "AND", Values: []any{map[string]any{"type": "XOR", "values": []any{match}}}}, wantUnresolved: true},
		{name: "nested unknown empty", group: PropertyGroup{Type: "OR", Values: []any{map[string]any{"type": "XOR", "values": []any{}}}}, wantUnresolved: true},
		{name: "negated cohort reference", group: PropertyGroup{Type: "OR", Values: []any{map[string]any{"type": "cohort", "value": "invalid", "negation": true}}}, wantUnresolved: true},
		{name: "missing nonempty type", group: PropertyGroup{Values: []any{match}}, wantUnresolved: true},
		{name: "AND matches", group: PropertyGroup{Type: "AND", Values: []any{match}}, want: true},
		{name: "AND mismatch", group: PropertyGroup{Type: "AND", Values: []any{match, mismatch}}},
		{name: "OR matches", group: PropertyGroup{Type: "OR", Values: []any{mismatch, match}}, want: true},
		{name: "OR mismatch", group: PropertyGroup{Type: "OR", Values: []any{mismatch}}},
		{name: "property matches", group: PropertyGroup{Type: "property", Values: []any{match}}, want: true},
		{name: "property mismatch", group: PropertyGroup{Type: "property", Values: []any{match, mismatch}}},
		{name: "empty AND", group: PropertyGroup{Type: "AND"}, want: true},
		{name: "empty OR", group: PropertyGroup{Type: "OR"}},
		{name: "empty property", group: PropertyGroup{Type: "property"}, want: true},
		{name: "canonical empty", group: PropertyGroup{}, want: true},
	}
	poller := &FeatureFlagsPoller{}
	cohorts := map[string]PropertyGroup{"invalid": {Type: "XOR", Values: []any{mismatch}}}
	for _, tt := range tests {
		for _, parsed := range []bool{false, true} {
			t.Run(tt.name+"/parsed="+valueToString(parsed), func(t *testing.T) {
				group := tt.group
				localCohorts := cohorts
				if parsed {
					group = preParsePG(group)
					localCohorts = preParseCohortValues(cohorts)
				}
				got, err := poller.matchPropertyGroup(group, Properties{"email": "user@x"}, localCohorts, nil, nil, "user", nil, nil)
				if tt.wantUnresolved {
					requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tt.want, got)
			})
		}
	}
}
