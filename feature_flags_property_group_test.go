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
			wantErr:    errCohortPropertyValue,
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
			name: "missing cohort requires server evaluation",
			group: PropertyGroup{Type: "AND", Values: []any{
				map[string]any{"type": "cohort", "value": "missing-cohort"},
			}},
			wantErr: errCohortRequiresServerEval,
		},
	}

	poller := &FeatureFlagsPoller{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawResult, rawErr := poller.matchPropertyGroup(tt.group, tt.properties, nil, nil, nil, "distinct-id", nil)
			require.Equal(t, tt.want, rawResult)
			require.Equal(t, tt.wantErr, rawErr)

			parsedGroup := preParsePG(tt.group)
			require.NotEmpty(t, parsedGroup.ParsedValues)
			parsedResult, parsedErr := poller.matchPropertyGroup(parsedGroup, tt.properties, nil, nil, nil, "distinct-id", nil)
			require.Equal(t, rawResult, parsedResult)
			require.Equal(t, rawErr, parsedErr)
		})
	}
}
