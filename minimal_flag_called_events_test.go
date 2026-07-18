package posthog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// minimalEventsFlagsResponse builds a v4 /flags response covering the three
// has_experiment shapes: false (plain-flag), true (experiment-flag), and
// absent (unknown-flag). When gated, the response carries the top-level
// minimalFlagCalledEvents field the server only sends when the gate is on.
func minimalEventsFlagsResponse(gated bool) string {
	gate := ""
	if gated {
		gate = `"minimalFlagCalledEvents": true,`
	}
	return fmt.Sprintf(`{
		%s
		"flags": {
			"plain-flag": {
				"key": "plain-flag",
				"enabled": true,
				"variant": null,
				"reason": {"code": "condition_match", "description": "Matched condition set 1", "condition_index": 0},
				"metadata": {"id": 1, "version": 2, "payload": "{\"foo\": 1}", "has_experiment": false}
			},
			"experiment-flag": {
				"key": "experiment-flag",
				"enabled": true,
				"variant": null,
				"reason": {"code": "condition_match", "description": "Matched condition set 1", "condition_index": 0},
				"metadata": {"id": 2, "version": 3, "payload": null, "has_experiment": true}
			},
			"unknown-flag": {
				"key": "unknown-flag",
				"enabled": true,
				"variant": null,
				"reason": {"code": "condition_match", "description": "Matched condition set 1", "condition_index": 0},
				"metadata": {"id": 3, "version": 4, "payload": null}
			}
		},
		"requestId": "req-42",
		"evaluatedAt": 1737312368000
	}`, gate)
}

// minimalEventsLocalDefinitions builds a local-evaluation definitions payload
// with the same three has_experiment shapes. When gated, the payload carries
// the top-level minimal_flag_called_events field.
func minimalEventsLocalDefinitions(gated bool) string {
	gate := ""
	if gated {
		gate = `"minimal_flag_called_events": true,`
	}
	return fmt.Sprintf(`{
		%s
		"flags": [
			{"id": 1, "key": "plain-flag", "active": true, "has_experiment": false, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}},
			{"id": 2, "key": "experiment-flag", "active": true, "has_experiment": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}},
			{"id": 3, "key": "unknown-flag", "active": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}}
		]
	}`, gate)
}

func newMinimalEventsRemoteServer(t *testing.T, flagsResponse string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			w.Write([]byte(flagsResponse))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newMinimalEventsLocalServer(t *testing.T, definitions string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			w.Write([]byte(definitions))
		case strings.HasPrefix(r.URL.Path, "/batch"):
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// assertExactPropertyKeys asserts the event carries exactly the expected
// property keys — the strict-allowlist guarantee of the minimal shape. want
// need not include system context ($os, $os_version, $os_distro,
// $go_version): those survive minimization and are appended automatically so
// the assertion stays host-agnostic (e.g. $os_distro is Linux-only).
func assertExactPropertyKeys(t *testing.T, event *CaptureInApi, want []string) {
	t.Helper()
	got := make([]string, 0, len(event.Properties))
	for k := range event.Properties {
		got = append(got, k)
	}
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	for k := range getSystemContext().ToProperties() {
		wantSorted = append(wantSorted, k)
	}
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Errorf("expected exactly property keys %v, got %v", wantSorted, got)
	}
}

// assertFullEventShape asserts the event kept the full legacy envelope:
// system context and DefaultEventProperties survive.
func assertFullEventShape(t *testing.T, event *CaptureInApi) {
	t.Helper()
	if _, ok := event.Properties["$os"]; !ok {
		t.Error("expected full event to carry system context ($os)")
	}
	if event.Properties["app_version"] != "1.2.3" {
		t.Errorf("expected full event to carry DefaultEventProperties, got app_version=%v", event.Properties["app_version"])
	}
}

func withDefaultEventProperties(c *Config) {
	c.DefaultEventProperties = NewProperties().Set("app_version", "1.2.3")
}

func TestGetFeatureFlag_Remote_GatedNoExperiment_SendsMinimalEvent(t *testing.T) {
	t.Parallel()
	server := newMinimalEventsRemoteServer(t, minimalEventsFlagsResponse(true))
	client, capture, _ := newEvalClient(t, server, withDefaultEventProperties)

	deviceId := "device-1"
	if _, err := client.GetFeatureFlag(FeatureFlagPayload{
		Key:        "plain-flag",
		DistinctId: "user-1",
		DeviceId:   &deviceId,
		Groups:     NewGroups().Set("company", "id:5"),
	}); err != nil {
		t.Fatalf("GetFeatureFlag error: %v", err)
	}

	events := waitForEventCount(capture, 1, 5*time.Second)
	event := findEvent(events, "$feature_flag_called", "plain-flag")
	if event == nil {
		t.Fatal("expected $feature_flag_called for plain-flag")
	}
	assertExactPropertyKeys(t, event, []string{
		"$feature_flag",
		"$feature_flag_response",
		"$feature_flag_has_experiment",
		"$feature_flag_id",
		"$feature_flag_version",
		"$feature_flag_reason",
		"$feature_flag_request_id",
		"$feature_flag_evaluated_at",
		"locally_evaluated",
		"$groups",
		"$device_id",
		"$geoip_disable",
		"$is_server",
		"$lib",
		"$lib_version",
	})
	if event.Properties["$device_id"] != "device-1" {
		t.Errorf("expected minimal event to keep $device_id, got %v", event.Properties["$device_id"])
	}
	if event.Properties["$is_server"] != true {
		t.Errorf("expected minimal event to keep $is_server=true, got %v", event.Properties["$is_server"])
	}
	if event.Properties["$feature_flag_has_experiment"] != false {
		t.Errorf("expected $feature_flag_has_experiment=false, got %v", event.Properties["$feature_flag_has_experiment"])
	}
	if event.Properties["locally_evaluated"] != false {
		t.Errorf("expected locally_evaluated=false, got %v", event.Properties["locally_evaluated"])
	}
}

func TestGetFeatureFlag_Remote_FullEventWhenSignalMissing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		gated   bool
		flagKey string
	}{
		{name: "gated flag with experiment", gated: true, flagKey: "experiment-flag"},
		{name: "gated flag with unknown has_experiment", gated: true, flagKey: "unknown-flag"},
		{name: "ungated flag without experiment", gated: false, flagKey: "plain-flag"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newMinimalEventsRemoteServer(t, minimalEventsFlagsResponse(test.gated))
			client, capture, _ := newEvalClient(t, server, withDefaultEventProperties)

			if _, err := client.GetFeatureFlag(FeatureFlagPayload{Key: test.flagKey, DistinctId: "user-1"}); err != nil {
				t.Fatalf("GetFeatureFlag error: %v", err)
			}

			events := waitForEventCount(capture, 1, 5*time.Second)
			event := findEvent(events, "$feature_flag_called", test.flagKey)
			if event == nil {
				t.Fatalf("expected $feature_flag_called for %s", test.flagKey)
			}
			assertFullEventShape(t, event)
		})
	}
}

func TestGetFeatureFlag_LocalEvaluation_GatedNoExperiment_SendsMinimalEvent(t *testing.T) {
	t.Parallel()
	server := newMinimalEventsLocalServer(t, minimalEventsLocalDefinitions(true))
	client, capture, _ := newEvalClient(t, server, withDefaultEventProperties, func(c *Config) {
		c.PersonalApiKey = "personal-key"
	})
	waitForFlagDefinitions(t, client)

	if _, err := client.GetFeatureFlag(FeatureFlagPayload{Key: "plain-flag", DistinctId: "user-1"}); err != nil {
		t.Fatalf("GetFeatureFlag error: %v", err)
	}

	events := waitForEventCount(capture, 1, 5*time.Second)
	event := findEvent(events, "$feature_flag_called", "plain-flag")
	if event == nil {
		t.Fatal("expected $feature_flag_called for plain-flag")
	}
	assertExactPropertyKeys(t, event, []string{
		"$feature_flag",
		"$feature_flag_response",
		"$feature_flag_has_experiment",
		"locally_evaluated",
		"$geoip_disable",
		"$is_server",
		"$lib",
		"$lib_version",
	})
	if event.Properties["locally_evaluated"] != true {
		t.Errorf("expected locally_evaluated=true, got %v", event.Properties["locally_evaluated"])
	}
}

func TestGetFeatureFlag_LocalEvaluation_FullEventWhenSignalMissing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		gated   bool
		flagKey string
	}{
		{name: "gated flag with experiment", gated: true, flagKey: "experiment-flag"},
		{name: "gated flag with unknown has_experiment", gated: true, flagKey: "unknown-flag"},
		{name: "ungated flag without experiment", gated: false, flagKey: "plain-flag"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newMinimalEventsLocalServer(t, minimalEventsLocalDefinitions(test.gated))
			client, capture, _ := newEvalClient(t, server, withDefaultEventProperties, func(c *Config) {
				c.PersonalApiKey = "personal-key"
			})
			waitForFlagDefinitions(t, client)

			if _, err := client.GetFeatureFlag(FeatureFlagPayload{Key: test.flagKey, DistinctId: "user-1"}); err != nil {
				t.Fatalf("GetFeatureFlag error: %v", err)
			}

			events := waitForEventCount(capture, 1, 5*time.Second)
			event := findEvent(events, "$feature_flag_called", test.flagKey)
			if event == nil {
				t.Fatalf("expected $feature_flag_called for %s", test.flagKey)
			}
			assertFullEventShape(t, event)
		})
	}
}

func TestEvaluateFlags_GatedNoExperiment_SendsMinimalEvent(t *testing.T) {
	t.Parallel()
	server := newMinimalEventsRemoteServer(t, minimalEventsFlagsResponse(true))
	client, capture, _ := newEvalClient(t, server, withDefaultEventProperties)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}
	snap.IsEnabled("plain-flag")
	snap.IsEnabled("experiment-flag")

	events := waitForEventCount(capture, 2, 5*time.Second)

	// The gated no-experiment flag sends the minimal shape; the snapshot
	// path's extras ($feature/<key>, $feature_flag_payload) are stripped.
	minimalEvent := findEvent(events, "$feature_flag_called", "plain-flag")
	if minimalEvent == nil {
		t.Fatal("expected $feature_flag_called for plain-flag")
	}
	assertExactPropertyKeys(t, minimalEvent, []string{
		"$feature_flag",
		"$feature_flag_response",
		"$feature_flag_has_experiment",
		"$feature_flag_id",
		"$feature_flag_version",
		"$feature_flag_reason",
		"$feature_flag_request_id",
		"$feature_flag_evaluated_at",
		"locally_evaluated",
		// EvaluateFlags normalizes nil Groups to an empty map, so $groups is
		// present (and empty) exactly as it is on full snapshot events.
		"$groups",
		"$geoip_disable",
		"$is_server",
		"$lib",
		"$lib_version",
	})

	// The experiment-linked flag keeps the full envelope.
	fullEvent := findEvent(events, "$feature_flag_called", "experiment-flag")
	if fullEvent == nil {
		t.Fatal("expected $feature_flag_called for experiment-flag")
	}
	assertFullEventShape(t, fullEvent)
	if fullEvent.Properties["$feature/experiment-flag"] != true {
		t.Errorf("expected full event to keep $feature/experiment-flag, got %v", fullEvent.Properties["$feature/experiment-flag"])
	}
}

func TestMinimalFlagCalledEvent_V1WireShape(t *testing.T) {
	t.Parallel()
	msg := Capture{
		DistinctId: "user-1",
		Event:      "$feature_flag_called",
		Properties: NewProperties().
			Set("$feature_flag", "plain-flag").
			Set("$feature_flag_response", true).
			Set("$feature_flag_has_experiment", false).
			Set("locally_evaluated", true).
			Set("$feature_flag_payload", `{"foo": 1}`).
			Set("custom_prop", "junk"),
		IsServer:               true,
		minimalFlagCalledEvent: true,
	}

	ev := buildV1Event(msg.apifyEvent(), nil)

	got := make([]string, 0, len(ev.Properties))
	for k := range ev.Properties {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"$feature_flag", "$feature_flag_has_experiment", "$feature_flag_response", "$is_server", "locally_evaluated"}
	for k := range getSystemContext().ToProperties() {
		want = append(want, k)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected exactly property keys %v, got %v", want, got)
	}
	if ev.Properties["$is_server"] != true {
		t.Errorf("expected minimal v1 event to keep $is_server=true, got %v", ev.Properties["$is_server"])
	}
}

func TestMinimalFlagCalledEvent_V1LiftsSessionId(t *testing.T) {
	t.Parallel()
	msg := Capture{
		DistinctId: "user-1",
		Event:      "$feature_flag_called",
		Properties: NewProperties().
			Set("$feature_flag", "plain-flag").
			Set("$feature_flag_response", true).
			Set("$feature_flag_has_experiment", false).
			Set(propertySessionID, "sess-1"),
		minimalFlagCalledEvent: true,
	}

	ev := buildV1Event(msg.apifyEvent(), nil)

	if ev.SessionId != "sess-1" {
		t.Errorf("expected $session_id to be lifted to the top-level session_id field, got %q", ev.SessionId)
	}
	if _, ok := ev.Properties[propertySessionID]; ok {
		t.Error("expected $session_id to be removed from properties after the v1 lift")
	}
}

// minimalEventsFallbackDefinitions returns definitions whose only flag is
// gated on a person property the request does not supply, so local evaluation
// is inconclusive and the SDK falls back to a remote /flags request.
func minimalEventsFallbackDefinitions(gated bool) string {
	gate := ""
	if gated {
		gate = `"minimal_flag_called_events": true,`
	}
	return fmt.Sprintf(`{
		%s
		"flags": [
			{"id": 1, "key": "plain-flag", "active": true, "has_experiment": false, "filters": {"groups": [{"properties": [{"key": "region", "operator": "exact", "value": ["USA"], "type": "person"}], "rollout_percentage": 100}]}}
		]
	}`, gate)
}

func TestGetFeatureFlag_RemoteFallback_GateComesFromFlagsResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		localGated  bool
		remoteGated bool
		wantMinimal bool
	}{
		{name: "local gate on but ungated remote response stays full", localGated: true, remoteGated: false, wantMinimal: false},
		{name: "local gate off but gated remote response goes minimal", localGated: false, remoteGated: true, wantMinimal: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definitions := minimalEventsFallbackDefinitions(test.localGated)
			flagsResponse := minimalEventsFlagsResponse(test.remoteGated)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
					w.Write([]byte(definitions))
				case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
					w.Write([]byte(flagsResponse))
				case strings.HasPrefix(r.URL.Path, "/batch"):
					w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected request to %s", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			client, capture, _ := newEvalClient(t, server, withDefaultEventProperties, func(c *Config) {
				c.PersonalApiKey = "personal-key"
			})
			waitForFlagDefinitions(t, client)

			if _, err := client.GetFeatureFlag(FeatureFlagPayload{Key: "plain-flag", DistinctId: "user-1"}); err != nil {
				t.Fatalf("GetFeatureFlag error: %v", err)
			}

			events := waitForEventCount(capture, 1, 5*time.Second)
			event := findEvent(events, "$feature_flag_called", "plain-flag")
			if event == nil {
				t.Fatal("expected $feature_flag_called for plain-flag")
			}
			if event.Properties["locally_evaluated"] != false {
				t.Errorf("expected the flag value to come from the remote fallback (locally_evaluated=false), got %v", event.Properties["locally_evaluated"])
			}
			if test.wantMinimal {
				assertExactPropertyKeys(t, event, []string{
					"$feature_flag",
					"$feature_flag_response",
					"$feature_flag_has_experiment",
					"locally_evaluated",
					"$geoip_disable",
					"$is_server",
					"$lib",
					"$lib_version",
				})
			} else {
				assertFullEventShape(t, event)
			}
		})
	}
}

func TestFetchNewFeatureFlags_304PreservesMinimalGate(t *testing.T) {
	t.Parallel()
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			requestCount++
			w.Header().Set("ETag", `"gate-etag"`)
			if requestCount == 1 {
				w.Write([]byte(minimalEventsLocalDefinitions(true)))
			} else {
				w.WriteHeader(http.StatusNotModified)
			}
		}
	}))
	t.Cleanup(server.Close)

	poller := newTestPoller(t, server.URL)
	poller.fetchNewFeatureFlags()
	if !poller.getMinimalFlagCalledEvents() {
		t.Fatal("expected the minimal gate to be set from the initial definitions fetch")
	}

	// A 304 refresh swaps in a new state carrying the refreshed ETag; the
	// gate must survive that swap.
	poller.fetchNewFeatureFlags()
	if requestCount != 2 {
		t.Fatalf("expected 2 definition requests, got %d", requestCount)
	}
	if !poller.getMinimalFlagCalledEvents() {
		t.Error("expected a 304 refresh to preserve the minimal gate")
	}
}
