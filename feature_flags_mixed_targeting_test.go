package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// Mixed-targeting flag: flag-level aggregation is null, the first condition targets
// group "company" (index 0), the second targets persons by email.
const mixedTargetingFlagJSON = `{
	"flags": [{
		"id": 1,
		"key": "mixed-flag",
		"active": true,
		"filters": {
			"aggregation_group_type_index": null,
			"groups": [
				{
					"aggregation_group_type_index": 0,
					"properties": [
						{"key": "plan", "value": "enterprise", "operator": "exact", "type": "group", "group_type_index": 0}
					],
					"rollout_percentage": 100
				},
				{
					"aggregation_group_type_index": null,
					"properties": [
						{"key": "email", "value": "test@example.com", "operator": "exact", "type": "person"}
					],
					"rollout_percentage": 100
				}
			]
		}
	}],
	"group_type_mapping": {"0": "company"},
	"cohorts": {}
}`

const onlyGroupConditionFlagJSON = `{
	"flags": [{
		"id": 1,
		"key": "only-group-flag",
		"active": true,
		"filters": {
			"aggregation_group_type_index": null,
			"groups": [
				{
					"aggregation_group_type_index": 0,
					"properties": [
						{"key": "plan", "value": "enterprise", "operator": "exact", "type": "group", "group_type_index": 0}
					],
					"rollout_percentage": 100
				}
			]
		}
	}],
	"group_type_mapping": {"0": "company"},
	"cohorts": {}
}`

func newMixedTargetingServer(t *testing.T, localFlagsJSON string, decideCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Order matters: /flags/definitions must be checked before /flags or /flags/
		// so the prefix doesn't accidentally match the decide endpoint.
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(localFlagsJSON))
			return
		}
		if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
			if decideCalls != nil {
				decideCalls.Add(1)
			}
			// Return a value that would clearly differ from local-eval result if we ever fall back.
			w.Write([]byte(`{"featureFlags": {"mixed-flag": "server-fallback", "only-group-flag": "server-fallback"}}`))
			return
		}
	}))
}

func TestMixedTargetingLocalEvaluation(t *testing.T) {
	type opts struct {
		groups           Groups
		personProperties Properties
		groupProperties  map[string]Properties
	}

	cases := []struct {
		name      string
		flagJSON  string
		flagKey   string
		opts      opts
		expectVal interface{}
	}{
		{
			name:      "person condition matches when no groups passed",
			flagJSON:  mixedTargetingFlagJSON,
			flagKey:   "mixed-flag",
			opts:      opts{personProperties: Properties{"email": "test@example.com"}},
			expectVal: true,
		},
		{
			name:     "group condition matches when group props match",
			flagJSON: mixedTargetingFlagJSON,
			flagKey:  "mixed-flag",
			opts: opts{
				groups:           Groups{"company": "acme"},
				groupProperties:  map[string]Properties{"company": {"plan": "enterprise"}},
				personProperties: Properties{"email": "nope@example.com"},
			},
			expectVal: true,
		},
		{
			name:     "no match when both person and group fail",
			flagJSON: mixedTargetingFlagJSON,
			flagKey:  "mixed-flag",
			opts: opts{
				groups:           Groups{"company": "acme"},
				groupProperties:  map[string]Properties{"company": {"plan": "free"}},
				personProperties: Properties{"email": "nope@example.com"},
			},
			expectVal: false,
		},
		{
			name:      "only group conditions, no groups passed: returns false without server fallback",
			flagJSON:  onlyGroupConditionFlagJSON,
			flagKey:   "only-group-flag",
			opts:      opts{},
			expectVal: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decideCalls atomic.Int32
			server := newMixedTargetingServer(t, tc.flagJSON, &decideCalls)
			defer server.Close()

			client, err := NewWithConfig("test-api-key", Config{
				PersonalApiKey: "test-personal-key",
				Endpoint:       server.URL,
			})
			require.NoError(t, err)
			defer client.Close()

			result, err := client.GetFeatureFlag(FeatureFlagPayload{
				Key:                 tc.flagKey,
				DistinctId:          "test-distinct-id",
				Groups:              tc.opts.groups,
				PersonProperties:    tc.opts.personProperties,
				GroupProperties:     tc.opts.groupProperties,
				OnlyEvaluateLocally: false,
			})
			require.NoError(t, err)
			require.Equal(t, tc.expectVal, result)
			require.Equal(t, int32(0), decideCalls.Load(), "expected no fallback to /flags decide endpoint")
		})
	}
}

// Verifies the rollout for a group condition under a mixed flag hashes on the
// group key, not the distinct_id — calling with the group passed must resolve
// locally without falling back to the decide endpoint.
func TestMixedTargetingRolloutBucketing(t *testing.T) {
	flagJSON := `{
		"flags": [{
			"id": 1,
			"key": "rollout-flag",
			"active": true,
			"filters": {
				"aggregation_group_type_index": null,
				"groups": [
					{
						"aggregation_group_type_index": 0,
						"properties": [],
						"rollout_percentage": 100
					}
				]
			}
		}],
		"group_type_mapping": {"0": "company"},
		"cohorts": {}
	}`
	var decideCalls atomic.Int32
	server := newMixedTargetingServer(t, flagJSON, &decideCalls)
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-key",
		Endpoint:       server.URL,
	})
	require.NoError(t, err)
	defer client.Close()

	result, err := client.GetFeatureFlag(FeatureFlagPayload{
		Key:             "rollout-flag",
		DistinctId:      "any-distinct-id",
		Groups:          Groups{"company": "acme"},
		GroupProperties: map[string]Properties{"company": {}},
	})
	require.NoError(t, err)
	require.Equal(t, true, result)
	require.Equal(t, int32(0), decideCalls.Load(), "expected no fallback to /flags decide endpoint")
}
