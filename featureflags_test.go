package posthog

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func TestCalculateHash(t *testing.T) {
	// calculateHash(key, distinctId, salt) computes SHA1(key + "." + distinctId + salt)
	// The "." is appended internally.
	for _, tt := range []struct {
		key   string
		ident string
		want  float64
	}{
		{"holdout-", "some_distinct_id", 0.0866397292395582},
		{"holdout-", "test-identifier", 0.7496340887209227},
		{"holdout-", "example_id", 0.8691395133214396},
		{"holdout-", "example_id2", 0.8442736553863017},
	} {
		t.Run(tt.ident, func(t *testing.T) {
			got := calculateHash(tt.key, tt.ident, "")
			if math.Abs(got-tt.want) > 0.000001 {
				t.Logf("got: %.16f, want: %f", got, tt.want)
				t.Fail()
			}
		})
	}
}

func TestFeatureFlagsPollerStateMapGetters(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	if got := poller.getCohorts(); len(got) != 0 {
		t.Fatalf("empty poller getCohorts() = %#v, want empty map", got)
	}
	if got := poller.getGroups(); len(got) != 0 {
		t.Fatalf("empty poller getGroups() = %#v, want empty map", got)
	}

	cohorts := map[string]PropertyGroup{"1": {Type: "AND"}}
	groups := map[string]string{"0": "company"}
	poller.state.Store(&flagsState{cohorts: cohorts, groups: groups})

	gotCohorts := poller.getCohorts()
	gotGroups := poller.getGroups()
	if len(gotCohorts) != 1 || gotCohorts["1"].Type != "AND" {
		t.Fatalf("getCohorts() = %#v, want stored cohort", gotCohorts)
	}
	if len(gotGroups) != 1 || gotGroups["0"] != "company" {
		t.Fatalf("getGroups() = %#v, want stored group mapping", gotGroups)
	}
}

const (
	planIsProFilter       = `{"key": "plan", "type": "person", "operator": "exact", "value": "pro"}`
	countryIsUSFilter     = `{"key": "country", "type": "person", "operator": "exact", "value": "US"}`
	unknownOperatorFilter = `{"key": "country", "type": "person", "operator": "is_on_the_moon", "value": "US"}`
	staticCohortFilter    = `{"key": "id", "type": "cohort", "value": 404}`
	ageOver18Filter       = `{"key": "age", "type": "person", "operator": "gt", "value": 18}`
)

func flagDefinition(key string, conditions ...string) string {
	return `{"key": "` + key + `", "active": true, "filters": {"groups": [` + strings.Join(conditions, ",") + `]}}`
}

func flagCondition(rollout int, properties ...string) string {
	return fmt.Sprintf(`{"properties": [%s], "rollout_percentage": %d}`, strings.Join(properties, ","), rollout)
}

func flagDependencyFilter(key string, value interface{}, chain ...string) string {
	encodedValue, _ := json.Marshal(value)
	encodedChain, _ := json.Marshal(chain)
	return fmt.Sprintf(`{"key": %q, "type": "flag", "operator": "flag_evaluates_to", "value": %s, "dependency_chain": %s}`, key, encodedValue, encodedChain)
}

func newDefinitionsPoller(t *testing.T, definitions string) *FeatureFlagsPoller {
	t.Helper()
	poller := newTestPoller(t, "http://localhost")
	poller.Logger = silentLogger{}
	decoded, err := decodeFlagDefinitions([]byte(definitions))
	require.NoError(t, err)
	poller.applyFlagDefinitions([]byte(definitions), decoded, "")
	return poller
}

func computeDefinedFlag(t *testing.T, definitions, key string, personProperties Properties) (interface{}, error) {
	t.Helper()
	poller := newDefinitionsPoller(t, definitions)
	state := poller.state.Load()
	flag, ok := state.flagsByKey[key]
	require.True(t, ok, "no definition for %s", key)
	return poller.computeFlagLocally(flag, "user-123", nil, Groups{}, personProperties, nil, state.cohorts, state)
}

func requireUnresolvedReason(t *testing.T, err error, want UnresolvedFlagReason) {
	t.Helper()
	reason, ok := unresolvedReasonOf(err)
	require.True(t, ok, "expected an inconclusive or server-required error, got %v", err)
	require.Equal(t, want, reason)
}

func TestPickUnresolvedReasonFollowsSpecPrecedence(t *testing.T) {
	specOrder := []UnresolvedFlagReason{
		UnresolvedFlagReasonExperienceContinuity,
		UnresolvedFlagReasonUnsupportedDefinition,
		UnresolvedFlagReasonUnresolvedDependency,
		UnresolvedFlagReasonMissingContext,
	}
	for i, higher := range specOrder {
		require.Equal(t, higher, pickUnresolvedReason("", higher))
		for _, lower := range specOrder[i:] {
			require.Equal(t, higher, pickUnresolvedReason(higher, lower), "%s then %s", higher, lower)
			require.Equal(t, higher, pickUnresolvedReason(lower, higher), "%s then %s", lower, higher)
		}
	}
	require.Equal(t, UnresolvedFlagReasonUnsupportedDefinition, pickUnresolvedReason(UnresolvedFlagReasonMissingContext, ""))
	requireUnresolvedReason(t, &InconclusiveMatchError{msg: "no reason"}, UnresolvedFlagReasonUnsupportedDefinition)
}

func TestMatchPropertyUnresolvedReasons(t *testing.T) {
	semver := func(operator string, value interface{}) FlagProperty {
		return FlagProperty{Key: "version", Operator: operator, Value: value}
	}
	tests := []struct {
		name       string
		property   FlagProperty
		properties Properties
		version    int
		want       UnresolvedFlagReason
		wantServer bool
	}{
		{name: "missing property", property: FlagProperty{Key: "plan", Operator: "exact", Value: "pro"}, want: UnresolvedFlagReasonMissingContext},
		{name: "unknown operator", property: FlagProperty{Key: "plan", Operator: "is_on_the_moon", Value: "pro"}, properties: Properties{"plan": "pro"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "unknown operator with missing property", property: FlagProperty{Key: "plan", Operator: "is_on_the_moon", Value: "pro"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "person semver value is not a string", property: semver("semver_gt", "1.0.0"), properties: Properties{"version": 1}, want: UnresolvedFlagReasonMissingContext},
		{name: "person semver value is invalid", property: semver("semver_gt", "1.0.0"), properties: Properties{"version": "latest"}, want: UnresolvedFlagReasonMissingContext},
		{name: "filter semver value is not a string", property: semver("semver_gt", 1), properties: Properties{"version": "1.0.0"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "filter semver value is invalid", property: semver("semver_tilde", "x.y"), properties: Properties{"version": "1.0.0"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "invalid person and filter semver values", property: semver("semver_eq", "x.y"), properties: Properties{"version": "latest"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "invalid filter semver value with missing property", property: semver("semver_caret", "x.y"), want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "ambiguous number", property: FlagProperty{Key: "age", Operator: "exact", Value: float64(30)}, properties: Properties{"age": "30"}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "ambiguous number with missing property", property: FlagProperty{Key: "age", Operator: "is_not", Value: float64(30)}, want: UnresolvedFlagReasonUnsupportedDefinition},
		{name: "partly ambiguous list with missing property", property: FlagProperty{Key: "age", Operator: "exact", Value: []interface{}{float64(30), "thirty"}}, want: UnresolvedFlagReasonMissingContext},
		{name: "regex requiring the server", property: FlagProperty{Key: "email", Operator: "regex", Value: `\bpro`}, properties: Properties{"email": "pro@example.com"}, want: UnresolvedFlagReasonUnsupportedDefinition, wantServer: true},
		{name: "regex requiring the server with missing property", property: FlagProperty{Key: "email", Operator: "regex", Value: `\bpro`}, want: UnresolvedFlagReasonUnsupportedDefinition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := matchProperty(tt.property, tt.properties, tt.version)
			require.False(t, matched)
			require.Equal(t, tt.wantServer, isServerEvalError(err), "%v", err)
			requireUnresolvedReason(t, err, tt.want)
		})
	}
}

func TestConditionLoopDoesNotStopAtFirstInconclusiveFilter(t *testing.T) {
	const dependsOnStaticCohort = `{"key": "cohort-dependency", "active": true, "filters": {"groups": [{"properties": [` + staticCohortFilter + `], "rollout_percentage": 100}]}}`
	earlyExit := func(conditions ...string) string {
		return `{"key": "checkout", "active": true, "filters": {"early_exit": true, "groups": [` + strings.Join(conditions, ",") + `]}}`
	}
	canada := NewProperties().Set("country", "CA")
	tests := []struct {
		name       string
		flags      []string
		properties Properties
		want       interface{}
		wantReason UnresolvedFlagReason
		wantServer bool
	}{
		{
			name:       "inconclusive filter then non-matching filter",
			flags:      []string{flagDefinition("checkout", flagCondition(100, planIsProFilter, countryIsUSFilter))},
			properties: canada,
			want:       false,
		},
		{
			name:       "static cohort then non-matching filter",
			flags:      []string{flagDefinition("checkout", flagCondition(100, staticCohortFilter, countryIsUSFilter))},
			properties: canada,
			want:       false,
		},
		{
			name:       "static cohort first in its condition requires the server",
			flags:      []string{flagDefinition("checkout", flagCondition(100, staticCohortFilter, planIsProFilter), flagCondition(100))},
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
			wantServer: true,
		},
		{
			name:  "static cohort after another inconclusive filter lets later conditions resolve",
			flags: []string{flagDefinition("checkout", flagCondition(100, planIsProFilter, staticCohortFilter), flagCondition(100))},
			want:  true,
		},
		{
			name:       "plain error after an inconclusive filter lets later conditions resolve",
			flags:      []string{flagDefinition("checkout", flagCondition(100, planIsProFilter, ageOver18Filter), flagCondition(100))},
			properties: NewProperties().Set("age", "unknown"),
			want:       true,
		},
		{
			name:       "plain error after a static cohort still requires the server",
			flags:      []string{flagDefinition("checkout", flagCondition(100, staticCohortFilter, ageOver18Filter), flagCondition(100))},
			properties: NewProperties().Set("age", "unknown"),
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
			wantServer: true,
		},
		{
			name:       "highest reason in a condition",
			flags:      []string{flagDefinition("checkout", flagCondition(100, planIsProFilter, unknownOperatorFilter))},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
		},
		{
			name:       "highest reason in a condition in the reverse order",
			flags:      []string{flagDefinition("checkout", flagCondition(100, unknownOperatorFilter, planIsProFilter))},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
		},
		{
			name:       "highest reason across conditions",
			flags:      []string{flagDefinition("checkout", flagCondition(100, unknownOperatorFilter), flagCondition(100, planIsProFilter))},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
		},
		{
			name: "server-required dependency does not override a higher reason",
			flags: []string{
				dependsOnStaticCohort,
				flagDefinition("checkout", flagCondition(100, unknownOperatorFilter), flagCondition(100, flagDependencyFilter("cohort-dependency", true, "cohort-dependency"))),
			},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
			wantServer: true,
		},
		{
			name: "server-required dependency does not hide a higher reason in a later condition",
			flags: []string{
				dependsOnStaticCohort,
				flagDefinition("checkout", flagCondition(100, flagDependencyFilter("cohort-dependency", true, "cohort-dependency")), flagCondition(100, unknownOperatorFilter)),
			},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
			wantServer: true,
		},
		{
			name: "server-required dependency then a higher reason in the same condition",
			flags: []string{
				dependsOnStaticCohort,
				flagDefinition("checkout", flagCondition(100, flagDependencyFilter("cohort-dependency", true, "cohort-dependency"), unknownOperatorFilter)),
			},
			properties: canada,
			wantReason: UnresolvedFlagReasonUnsupportedDefinition,
			wantServer: true,
		},
		{
			name:       "early exit after a condition ruled out by a later filter",
			flags:      []string{earlyExit(flagCondition(100, planIsProFilter, countryIsUSFilter), flagCondition(0))},
			properties: canada,
			want:       false,
		},
		{
			name:       "early exit after an inconclusive condition",
			flags:      []string{earlyExit(flagCondition(100, planIsProFilter), flagCondition(0))},
			wantReason: UnresolvedFlagReasonMissingContext,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeDefinedFlag(t, localDefinitions(tt.flags...), "checkout", tt.properties)
			if tt.wantReason == "" {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return
			}
			require.Equal(t, tt.wantServer, isServerEvalError(err), "%v", err)
			requireUnresolvedReason(t, err, tt.wantReason)
		})
	}
}

func TestInactiveExperienceContinuityFlagResolvesToFalse(t *testing.T) {
	inactive := `{"key": "checkout", "active": false, "ensure_experience_continuity": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}}`
	got, err := computeDefinedFlag(t, localDefinitions(inactive), "checkout", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)

	_, err = computeDefinedFlag(t, localDefinitions(continuityCheckoutDefinition), "checkout", nil)
	requireUnresolvedReason(t, err, UnresolvedFlagReasonExperienceContinuity)
}

func TestHoldoutWithoutDeviceIDReportsHighestReason(t *testing.T) {
	holdoutFlag := func(properties ...string) string {
		return `{"key": "checkout", "active": true, "bucketing_identifier": "device_id", "filters": {"holdout": {"id": 1, "exclusion_percentage": 50}, "groups": [` + flagCondition(100, properties...) + `]}}`
	}
	_, err := computeDefinedFlag(t, localDefinitions(holdoutFlag()), "checkout", nil)
	requireUnresolvedReason(t, err, UnresolvedFlagReasonMissingContext)

	_, err = computeDefinedFlag(t, localDefinitions(holdoutFlag(staticCohortFilter)), "checkout", nil)
	requireUnresolvedReason(t, err, UnresolvedFlagReasonUnsupportedDefinition)
}

// definitionsSequenceServer serves each body in turn on /flags/definitions, repeating
// the last one, and counts remote evaluation requests. A body of "304" answers with
// Not Modified, and "402" with a quota limit.
func definitionsSequenceServer(t *testing.T, bodies ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var definitionRequests atomic.Int32
	var remoteRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			remoteRequests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		index := int(definitionRequests.Add(1)) - 1
		if index >= len(bodies) {
			index = len(bodies) - 1
		}
		if bodies[index] == "304" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if bodies[index] == "402" {
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, index))
		w.Write([]byte(bodies[index]))
	}))
	t.Cleanup(server.Close)
	return server, &remoteRequests
}

func countUnresolvableWarnings(logger *captureLogger, key string, reason UnresolvedFlagReason) int {
	count := 0
	for _, warning := range logger.snapshot() {
		if strings.Contains(warning, key) && strings.Contains(warning, string(reason)) {
			count++
		}
	}
	return count
}

func TestUnresolvableFlagWarningIsLoggedOncePerDefinition(t *testing.T) {
	definitions := localDefinitions(continuityCheckoutDefinition)
	// The same definition serialized differently, as a shared cache may store it.
	reserialized := `{"cohorts":{},"flags":[{"filters":{"groups":[{"rollout_percentage":100,"properties":[]}]},"ensure_experience_continuity":true,"active":true,"key":"checkout"}],"group_type_mapping":{}}`
	tests := []struct {
		name   string
		bodies []string
		cache  bool
	}{
		{name: "unchanged definitions", bodies: []string{definitions, definitions, definitions}},
		{name: "304 not modified", bodies: []string{definitions, "304", "304"}},
		{name: "successful shared-cache load", bodies: []string{definitions}, cache: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, remoteRequests := definitionsSequenceServer(t, tt.bodies...)
			logger := &captureLogger{}
			poller := newTestPoller(t, server.URL)
			provider := &fakeFlagDefinitionCache{shouldFetch: true}
			if tt.cache {
				poller = newCachingTestPoller(t, server.URL, provider)
			}
			poller.Logger = logger
			poller.fetchNewFeatureFlags()
			// With a cache provider, later polls load from the shared cache instead of the API.
			provider.mu.Lock()
			provider.shouldFetch = false
			provider.cached = json.RawMessage(reserialized)
			provider.mu.Unlock()

			for i := 0; i < 2; i++ {
				poller.fetchNewFeatureFlags()
			}

			if tt.cache {
				_, cacheLoads, _, _ := provider.calls()
				require.Equal(t, 2, cacheLoads)
			}
			require.Equal(t, 1, countUnresolvableWarnings(logger, "checkout", UnresolvedFlagReasonExperienceContinuity), logger.snapshot())
			require.Len(t, logger.snapshot(), 1)
			require.Zero(t, remoteRequests.Load())
		})
	}
}

func TestUnresolvableFlagWarningRepeatsWhenDefinitionChanges(t *testing.T) {
	changed := `{"key": "checkout", "active": true, "ensure_experience_continuity": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 50}]}}`
	server, _ := definitionsSequenceServer(t, localDefinitions(continuityCheckoutDefinition), localDefinitions(changed), localDefinitions(changed))
	logger := &captureLogger{}
	poller := newTestPoller(t, server.URL)
	poller.Logger = logger

	for i := 0; i < 3; i++ {
		poller.fetchNewFeatureFlags()
	}

	require.Equal(t, 2, countUnresolvableWarnings(logger, "checkout", UnresolvedFlagReasonExperienceContinuity), logger.snapshot())
}

func TestUnresolvableFlagWarningForgetsRemovedFlags(t *testing.T) {
	withCheckout := localDefinitions(betaUIDefinition, continuityCheckoutDefinition)
	tests := []struct {
		name    string
		removal string
	}{
		{name: "removed from the definitions", removal: localDefinitions(betaUIDefinition)},
		{name: "cleared by a quota limit", removal: "402"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := definitionsSequenceServer(t, withCheckout, tt.removal, withCheckout)
			logger := &captureLogger{}
			poller := newTestPoller(t, server.URL)
			poller.Logger = logger

			for i := 0; i < 3; i++ {
				poller.fetchNewFeatureFlags()
			}

			require.Equal(t, 2, countUnresolvableWarnings(logger, "checkout", UnresolvedFlagReasonExperienceContinuity), logger.snapshot())
			require.Len(t, poller.warnedUnresolvableFlags, 1)
		})
	}
}

func TestUnresolvableFlagWarningCoversUnsupportedDefinitions(t *testing.T) {
	inactiveContinuity := `{"key": "inactive-continuity", "active": false, "ensure_experience_continuity": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}}`
	definitions := localDefinitions(
		continuityCheckoutDefinition,
		inactiveContinuity,
		flagDefinition("static-cohort", flagCondition(100, staticCohortFilter)),
		flagDefinition("unknown-operator", flagCondition(100, unknownOperatorFilter)),
		flagDefinition("malformed-semver", flagCondition(100, `{"key": "version", "type": "person", "operator": "semver_gt", "value": "x.y"}`)),
		flagDefinition("missing-dependency", flagCondition(100, flagDependencyFilter("gone", true, "gone"))),
		flagDefinition("circular-dependency", flagCondition(100, flagDependencyFilter("circular-dependency", true))),
		flagDefinition("needs-context", flagCondition(100, planIsProFilter)),
		flagDefinition("depends-on-continuity", flagCondition(100, flagDependencyFilter("checkout", true, "checkout"))),
	)
	server, _ := definitionsSequenceServer(t, definitions)
	logger := &captureLogger{}
	poller := newTestPoller(t, server.URL)
	poller.Logger = logger

	poller.fetchNewFeatureFlags()
	poller.fetchNewFeatureFlags()

	warnings := logger.snapshot()
	require.Len(t, warnings, 2)
	require.Contains(t, warnings[0], `"checkout"`)
	require.Contains(t, warnings[0], string(UnresolvedFlagReasonExperienceContinuity))
	require.Contains(t, warnings[1], string(UnresolvedFlagReasonUnsupportedDefinition))
	require.Contains(t, warnings[1], `"static-cohort, unknown-operator, malformed-semver, missing-dependency, circular-dependency"`)
}
