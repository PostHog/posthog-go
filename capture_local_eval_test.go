package posthog

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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
const captureLocalEvalWaitTimeout = 5 * time.Second

type captureLocalEvalServer struct {
	server      *httptest.Server
	decideCalls atomic.Int32
}

func newCaptureLocalEvalServer(t *testing.T, definitionsFixture, decideResponse string) *captureLocalEvalServer {
	t.Helper()
	s := &captureLocalEvalServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
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

// waitForFlagDefinitions blocks until the poller's initial definitions fetch
// completes (success or failure), so a subsequent capture evaluates against
// settled poller state rather than racing the background fetch. The default
// capture path reads poller state non-blockingly, so without this a capture
// could run before definitions land and fall back to the remote /flags request.
func waitForFlagDefinitions(t *testing.T, c Client) {
	t.Helper()
	// GetFeatureFlags blocks on the initial fetch; the error (e.g. definitions
	// that fail to load) is intentionally ignored — we only need the fetch to settle.
	_, _ = c.(*client).featureFlagsPoller.GetFeatureFlags()
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
			waitForFlagDefinitions(t, client)

			if err := client.Enqueue(Capture{
				Event:            "test_event",
				DistinctId:       "distinct-id",
				SendFeatureFlags: tt.sendFlags,
			}); err != nil {
				t.Fatal(err)
			}

			event := capture.waitForEvent(captureLocalEvalWaitTimeout)
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
				// wantActiveFlags is kept sorted; $active_feature_flags is sorted for
				// deterministic output, so assert exact ordered equality.
				if !reflect.DeepEqual(active, tt.wantActiveFlags) {
					t.Errorf("$active_feature_flags = %v, want %v", active, tt.wantActiveFlags)
				}
			}
		})
	}
}

// TestCaptureSendFeatureFlagsFallsBackForStaticCohort verifies that a flag gated on
// a static cohort still triggers the remote /flags fallback during capture enrichment.
func TestCaptureSendFeatureFlagsFallsBackForStaticCohort(t *testing.T) {
	t.Parallel()
	assertCaptureFeatureFlagFallback(t, `{
		"flags": [{"id": 1, "key": "static-cohort-flag", "active": true, "filters": {"groups": [{"properties": [{"key": "id", "value": 999, "type": "cohort"}], "rollout_percentage": 100}]}}],
		"cohorts": {}
	}`, `{"featureFlags": {"static-cohort-flag": true}}`, "$feature/static-cohort-flag", true)
}

// TestCaptureSendFeatureFlagsFallsBackForGroupFlagWithoutGroups verifies that a
// group-aggregated flag captured without the relevant group context defers to /flags.
func TestCaptureSendFeatureFlagsFallsBackForGroupFlagWithoutGroups(t *testing.T) {
	t.Parallel()
	assertCaptureFeatureFlagFallback(t, `{
		"flags": [{"id": 1, "key": "group-flag", "active": true, "filters": {"aggregation_group_type_index": 0, "groups": [{"properties": [], "rollout_percentage": 100}]}}],
		"group_type_mapping": {"0": "company"},
		"cohorts": {}
	}`, `{"featureFlags": {"group-flag": "group-value"}}`, "$feature/group-flag", "group-value")
}

func assertCaptureFeatureFlagFallback(t *testing.T, definitions, flagsResponse, featureProperty string, want interface{}) {
	t.Helper()
	var decideCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			w.Write([]byte(definitions))
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			decideCalls.Add(1)
			w.Write([]byte(flagsResponse))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	client, capture, _ := newEvalClient(t, server, func(c *Config) { c.PersonalApiKey = "personal-key" })
	waitForFlagDefinitions(t, client)

	if err := client.Enqueue(Capture{Event: "test_event", DistinctId: "distinct-id", SendFeatureFlags: SendFeatureFlags(true)}); err != nil {
		t.Fatal(err)
	}
	event := capture.waitForEvent(captureLocalEvalWaitTimeout)
	if event == nil {
		t.Fatal("timed out waiting for captured event")
	}
	if got := decideCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 remote /flags fallback, got %d", got)
	}
	if event.Properties[featureProperty] != want {
		t.Errorf("expected %s=%v from remote fallback, got %v", featureProperty, want, event.Properties[featureProperty])
	}
}
