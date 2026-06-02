package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// captureLocalEvalServer is a mock PostHog endpoint that distinguishes local
// evaluation definition fetches (/flags/definitions) from remote /flags (decide)
// requests so capture-enrichment tests can assert whether a network /flags call
// was made. A definitionsFixture of "" makes the definitions endpoint fail,
// simulating definitions that never load.
type captureLocalEvalServer struct {
	server          *httptest.Server
	definitionLoads atomic.Int32
	decideCalls     atomic.Int32
}

func newCaptureLocalEvalServer(t *testing.T, definitionsFixture, decideResponse string) *captureLocalEvalServer {
	t.Helper()
	s := &captureLocalEvalServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			s.definitionLoads.Add(1)
			if definitionsFixture == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(fixture(definitionsFixture)))
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			s.decideCalls.Add(1)
			w.Write([]byte(decideResponse))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

// waitForDefinitionFetch blocks until the poller has attempted to load local flag
// definitions at least once, so a subsequent capture evaluates deterministically.
func (s *captureLocalEvalServer) waitForDefinitionFetch(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.definitionLoads.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local evaluation poller never fetched /flags/definitions")
}

// TestCaptureSendFeatureFlagsLocalEvaluation verifies that capture enrichment
// prefers local evaluation and only contacts the remote /flags endpoint when a
// flag can't be computed locally, while OnlyEvaluateLocally stays strictly local.
func TestCaptureSendFeatureFlagsLocalEvaluation(t *testing.T) {
	// test-multiple-flags-valid.json: beta-feature (rollout 100% -> true),
	// disabled-feature (rollout 0% -> false). Both resolvable locally.
	const validDefs = "feature_flag/test-multiple-flags-valid.json"
	// test-multiple-flags.json adds beta-feature2, gated on a person property
	// (country=US) the capture doesn't supply, so it can't be computed locally.
	const mixedDefs = "feature_flag/test-multiple-flags.json"
	const unusedDecide = `{"featureFlags": {"should-not": "be-used"}}`

	tests := []struct {
		name            string
		definitions     string // "" => definitions endpoint fails to load
		decideResponse  string
		sendFlags       SendFeatureFlagsValue
		wantDecideCalls int32
		wantProps       map[string]interface{}
		wantAbsent      []string
		wantActiveFlags []string // nil => skip the $active_feature_flags check
	}{
		{
			name:            "prefers local evaluation with no remote request",
			definitions:     validDefs,
			decideResponse:  unusedDecide,
			sendFlags:       SendFeatureFlags(true),
			wantDecideCalls: 0,
			wantProps: map[string]interface{}{
				"$feature/beta-feature": true,
				// A disabled flag is still attached as $feature/<key>=false …
				"$feature/disabled-feature": false,
			},
			// … but excluded from $active_feature_flags.
			wantActiveFlags: []string{"beta-feature"},
		},
		{
			name:            "falls back to remote for an uncomputable flag",
			definitions:     mixedDefs,
			decideResponse:  `{"featureFlags": {"beta-feature": "decide-fallback-value", "beta-feature2": "variant-2", "disabled-feature": false}}`,
			sendFlags:       SendFeatureFlags(true),
			wantDecideCalls: 1,
			wantProps: map[string]interface{}{
				// Remote evaluation is authoritative for the flags it returns.
				"$feature/beta-feature":  "decide-fallback-value",
				"$feature/beta-feature2": "variant-2",
				// disabled-feature stays attached as false, excluded from active below.
				"$feature/disabled-feature": false,
			},
			wantActiveFlags: []string{"beta-feature", "beta-feature2"},
		},
		{
			name:           "remote turns off a locally-true flag",
			definitions:    mixedDefs,
			decideResponse: `{"featureFlags": {"beta-feature": false, "beta-feature2": "variant-2"}}`,
			sendFlags:      SendFeatureFlags(true),
			// beta-feature2 can't be computed locally, forcing a fallback. beta-feature
			// resolves true locally but the remote turns it off, so it lands as
			// $feature/beta-feature=false and drops out of $active_feature_flags.
			wantDecideCalls: 1,
			wantProps: map[string]interface{}{
				"$feature/beta-feature":  false,
				"$feature/beta-feature2": "variant-2",
			},
			wantActiveFlags: []string{"beta-feature2"},
		},
		{
			name:            "falls back to remote when definitions fail to load",
			definitions:     "",
			decideResponse:  `{"featureFlags": {"remote-flag": true}}`,
			sendFlags:       SendFeatureFlags(true),
			wantDecideCalls: 1,
			wantProps:       map[string]interface{}{"$feature/remote-flag": true},
		},
		{
			name:            "OnlyEvaluateLocally stays strictly local",
			definitions:     mixedDefs,
			decideResponse:  unusedDecide,
			sendFlags:       &SendFeatureFlagsOptions{OnlyEvaluateLocally: true},
			wantDecideCalls: 0,
			wantProps: map[string]interface{}{
				"$feature/beta-feature":     true,
				"$feature/disabled-feature": false,
			},
			// beta-feature2 can't be computed locally and must not appear without a fallback.
			wantAbsent:      []string{"$feature/beta-feature2"},
			wantActiveFlags: []string{"beta-feature"},
		},
	}

	for _, tt := range tests {
		tt := tt // Capture loop variable for Go 1.21 compatibility
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newCaptureLocalEvalServer(t, tt.definitions, tt.decideResponse)
			client, capture, _ := newEvalClient(t, server.server, func(c *Config) {
				c.PersonalApiKey = "personal-key"
			})
			server.waitForDefinitionFetch(t)

			if err := client.Enqueue(Capture{
				Event:            "test_event",
				DistinctId:       "distinct-id",
				SendFeatureFlags: tt.sendFlags,
			}); err != nil {
				t.Fatal(err)
			}

			event := capture.waitForEvent(2 * time.Second)
			if event == nil {
				t.Fatal("timed out waiting for captured event")
			}

			if got := server.decideCalls.Load(); got != tt.wantDecideCalls {
				t.Errorf("decide calls = %d, want %d", got, tt.wantDecideCalls)
			}
			for key, want := range tt.wantProps {
				if got := event.Properties[key]; got != want {
					t.Errorf("event property %s = %v, want %v", key, got, want)
				}
			}
			for _, key := range tt.wantAbsent {
				if _, ok := event.Properties[key]; ok {
					t.Errorf("expected event property %s to be absent", key)
				}
			}
			if tt.wantActiveFlags != nil {
				active, ok := event.Properties["$active_feature_flags"].([]string)
				if !ok {
					t.Fatalf("expected $active_feature_flags to be []string, got %T", event.Properties["$active_feature_flags"])
				}
				got := make(map[string]bool, len(active))
				for _, k := range active {
					got[k] = true
				}
				if len(got) != len(tt.wantActiveFlags) {
					t.Errorf("$active_feature_flags = %v, want %v", active, tt.wantActiveFlags)
				}
				for _, k := range tt.wantActiveFlags {
					if !got[k] {
						t.Errorf("$active_feature_flags = %v, missing %s", active, k)
					}
				}
			}
		})
	}
}

// TestCaptureSendFeatureFlagsFallsBackForStaticCohort verifies that a flag gated on
// a static cohort — which can't be evaluated locally and returns
// RequiresServerEvaluationError rather than InconclusiveMatchError — still triggers
// the remote /flags fallback during capture enrichment, instead of erroring and
// dropping enrichment for the whole event.
func TestCaptureSendFeatureFlagsFallsBackForStaticCohort(t *testing.T) {
	t.Parallel()
	var definitionLoads, decideCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			definitionLoads.Add(1)
			// Flag gated on cohort 999 with no cohort definitions, so it's a static
			// cohort that requires server evaluation.
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"key": "static-cohort-flag",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{"key": "id", "value": 999, "type": "cohort"}
									],
									"rollout_percentage": 100
								}
							]
						}
					}
				],
				"cohorts": {}
			}`))
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			decideCalls.Add(1)
			w.Write([]byte(`{"featureFlags": {"static-cohort-flag": true}}`))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, capture, _ := newEvalClient(t, server, func(c *Config) {
		c.PersonalApiKey = "personal-key"
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && definitionLoads.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "distinct-id",
		SendFeatureFlags: SendFeatureFlags(true),
	}); err != nil {
		t.Fatal(err)
	}

	event := capture.waitForEvent(2 * time.Second)
	if event == nil {
		t.Fatal("timed out waiting for captured event")
	}
	if got := decideCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 remote /flags fallback for the static cohort, got %d", got)
	}
	if event.Properties["$feature/static-cohort-flag"] != true {
		t.Errorf("expected $feature/static-cohort-flag=true from remote fallback, got %v", event.Properties["$feature/static-cohort-flag"])
	}
}

// TestCaptureSendFeatureFlagsFallsBackForGroupFlagWithoutGroups verifies that a
// group-aggregated flag captured without the relevant group context — which
// computeFlagLocally reports as a plain error (not Inconclusive/ServerEval) — still
// defers to the remote /flags request rather than dropping enrichment for the event.
func TestCaptureSendFeatureFlagsFallsBackForGroupFlagWithoutGroups(t *testing.T) {
	t.Parallel()
	var definitionLoads, decideCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			definitionLoads.Add(1)
			// Flag aggregated on the "company" group. The capture below passes no
			// groups, so it can't be computed locally.
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"key": "group-flag",
						"active": true,
						"filters": {
							"aggregation_group_type_index": 0,
							"groups": [
								{"properties": [], "rollout_percentage": 100}
							]
						}
					}
				],
				"group_type_mapping": {"0": "company"},
				"cohorts": {}
			}`))
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			decideCalls.Add(1)
			w.Write([]byte(`{"featureFlags": {"group-flag": "group-value"}}`))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, capture, _ := newEvalClient(t, server, func(c *Config) {
		c.PersonalApiKey = "personal-key"
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && definitionLoads.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "distinct-id",
		SendFeatureFlags: SendFeatureFlags(true),
	}); err != nil {
		t.Fatal(err)
	}

	event := capture.waitForEvent(2 * time.Second)
	if event == nil {
		t.Fatal("timed out waiting for captured event")
	}
	if got := decideCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 remote /flags fallback for the group flag, got %d", got)
	}
	if event.Properties["$feature/group-flag"] != "group-value" {
		t.Errorf("expected $feature/group-flag=group-value from remote fallback, got %v", event.Properties["$feature/group-flag"])
	}
}
