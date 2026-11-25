package posthog

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// Groups maps a group type to a string key, so the empty string is the only
// unusable group key a caller can pass. It counts as no group key.

func TestGroupFlagWithEmptyGroupKeyIsFalse(t *testing.T) {
	t.Parallel()
	emptyKey := Groups{"company": ""}
	for _, localOnly := range []bool{true, false} {
		localOnly := localOnly
		t.Run("local only "+strconv.FormatBool(localOnly), func(t *testing.T) {
			t.Parallel()
			client, capture, remoteCalls := newUnresolvedFlagsClient(t, companyCheckoutDefinitions(false), remoteCheckoutEnabled)
			payload := FeatureFlagPayload{Key: "checkout", DistinctId: "user-123", Groups: emptyKey, OnlyEvaluateLocally: localOnly}

			value, err := client.GetFeatureFlag(payload)
			require.NoError(t, err)
			require.Equal(t, false, value)
			require.Empty(t, flagCalledError(t, client, capture, "checkout"))

			enabled, err := client.IsFeatureEnabled(payload)
			require.NoError(t, err)
			require.Equal(t, false, enabled)

			all, err := client.GetAllFlags(FeatureFlagPayloadNoKey{DistinctId: "user-123", Groups: emptyKey, OnlyEvaluateLocally: localOnly})
			require.NoError(t, err)
			require.Equal(t, map[string]interface{}{"checkout": false}, all)

			snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", Groups: emptyKey, OnlyEvaluateLocally: localOnly})
			require.NoError(t, err)
			require.Empty(t, snapshot.UnresolvedFlags())
			require.Equal(t, false, snapshot.GetFlag("checkout"))

			require.Zero(t, remoteCalls.Load())
		})
	}
}

// An empty group key is no group key even when the group's properties are supplied.
func TestGroupFlagWithEmptyGroupKeyAndGroupPropertiesIsFalse(t *testing.T) {
	t.Parallel()
	definitions := companyCheckoutDefinitions(false, `{"key": "plan", "type": "group", "group_type_index": 0, "operator": "exact", "value": "pro"}`)
	client, _, remoteCalls := newUnresolvedFlagsClient(t, definitions, remoteCheckoutEnabled)

	snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{
		DistinctId:      "user-123",
		Groups:          Groups{"company": ""},
		GroupProperties: map[string]Properties{"company": {"plan": "pro"}},
	})
	require.NoError(t, err)
	require.Empty(t, snapshot.UnresolvedFlags())
	require.Equal(t, false, snapshot.GetFlag("checkout"))
	require.Zero(t, remoteCalls.Load())
}

func TestMixedTargetingSkipsGroupConditionWithEmptyGroupKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		groupProperties map[string]Properties
		email           string
		want            interface{}
	}{
		{"person condition matches", nil, "test@example.com", true},
		{"person condition fails", nil, "nope@example.com", false},
		{"matching group properties do not match the group condition", map[string]Properties{"company": {"plan": "enterprise"}}, "nope@example.com", false},
		{"person condition matches with group properties", map[string]Properties{"company": {"plan": "enterprise"}}, "test@example.com", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var decideCalls atomic.Int32
			server := newMixedTargetingServer(t, mixedTargetingFlagJSON, &decideCalls)
			t.Cleanup(server.Close)
			client, err := NewWithConfig("test-api-key", Config{PersonalApiKey: "test-personal-key", Endpoint: server.URL})
			require.NoError(t, err)
			t.Cleanup(func() { client.Close() })

			result, err := client.GetFeatureFlag(FeatureFlagPayload{
				Key:              "mixed-flag",
				DistinctId:       "test-distinct-id",
				Groups:           Groups{"company": ""},
				PersonProperties: Properties{"email": tt.email},
				GroupProperties:  tt.groupProperties,
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, result)
			require.Zero(t, decideCalls.Load(), "expected no fallback to /flags")
		})
	}
}

func TestGroupDependencyWithEmptyGroupKeyIsFalse(t *testing.T) {
	t.Parallel()
	orgParent := orgFlagDefinition("org-parent", flagCondition(100))
	personChild := flagDefinition("person-child", flagCondition(100, flagDependencyFilter("org-parent", true, "org-parent")))
	personChildExpectsFalse := flagDefinition("person-child-expects-false", flagCondition(100, flagDependencyFilter("org-parent", false, "org-parent")))
	client, _, remoteCalls := newUnresolvedFlagsClient(t, knownAbsentDefinitions(orgParent, personChild, personChildExpectsFalse), remoteNoFlags)

	snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", Groups: Groups{"organization": ""}})
	require.NoError(t, err)
	require.Empty(t, snapshot.UnresolvedFlags())
	require.Equal(t, false, snapshot.GetFlag("org-parent"))
	require.Equal(t, false, snapshot.GetFlag("person-child"))
	require.Equal(t, true, snapshot.GetFlag("person-child-expects-false"))
	require.Zero(t, remoteCalls.Load())
}

func TestCaptureSendFeatureFlagsResolvesGroupFlagWithEmptyGroupKeyAsFalse(t *testing.T) {
	t.Parallel()
	var decideCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			w.Write([]byte(companyCheckoutDefinitions(false)))
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			decideCalls.Add(1)
			w.Write([]byte(`{"featureFlags": {"checkout": "server-value"}}`))
		}
	}))
	t.Cleanup(server.Close)
	client, capture, _ := newEvalClient(t, server, func(c *Config) { c.PersonalApiKey = "personal-key" })
	waitForFlagDefinitions(t, client)

	require.NoError(t, client.Enqueue(Capture{Event: "test_event", DistinctId: "distinct-id", Groups: Groups{"company": ""}, SendFeatureFlags: SendFeatureFlags(true)}))
	event := capture.waitForEvent(captureLocalEvalWaitTimeout)
	require.NotNil(t, event, "timed out waiting for captured event")
	require.Equal(t, false, event.Properties["$feature/checkout"])
	require.Zero(t, decideCalls.Load())
}

// A usable group key keeps its existing evaluation. TestMixedTargetingRolloutBucketing
// covers its bucketing.
func TestGroupFlagWithUsableGroupKeyIsUnchanged(t *testing.T) {
	t.Parallel()
	needsPlan := `{"key": "plan", "type": "group", "group_type_index": 0, "operator": "exact", "value": "pro"}`
	tests := []struct {
		name        string
		definitions string
		groups      Groups
		want        interface{}
		wantReason  UnresolvedFlagReason
	}{
		{"string key", companyCheckoutDefinitions(false), Groups{"company": "acme"}, true, ""},
		{"numeric string key", companyCheckoutDefinitions(false), Groups{"company": "0"}, true, ""},
		{"usable key without the needed group properties", companyCheckoutDefinitions(false, needsPlan), Groups{"company": "acme"}, nil, UnresolvedFlagReasonMissingContext},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _, remoteCalls := newUnresolvedFlagsClient(t, tt.definitions, remoteNoFlags)
			snapshot, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", Groups: tt.groups, OnlyEvaluateLocally: true})
			require.NoError(t, err)
			require.Equal(t, tt.want, snapshot.GetFlag("checkout"))
			if tt.wantReason == "" {
				require.Empty(t, snapshot.UnresolvedFlags())
			} else {
				require.Equal(t, map[string]UnresolvedFlagReason{"checkout": tt.wantReason}, snapshot.UnresolvedFlags())
			}
			require.Zero(t, remoteCalls.Load())
		})
	}
}
