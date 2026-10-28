package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go/v2"
)

func TestPostFlagsWithRetryRetriesRetryableStatusThenSucceeds(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodPost)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"flag_keys_to_evaluate":["example"]}` {
			t.Fatalf("body = %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"featureFlags":{"example":true}}`))
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{"flag_keys_to_evaluate":["example"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestPostFlagsWithRetryStopsAfterRetryableFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{}`))
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error")
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestPostFlagsWithRetryDoesNotRetryNonRetryableStatus(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	resp, err := postFlagsWithRetry(server.URL, []byte(`{}`))
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error")
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestBuildCaptureKeepsAValidUUIDAndReplacesOthers(t *testing.T) {
	supplied := "0190d4a3-8f2b-7c3e-9a1d-5b6c7d8e9f00"
	if got := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", UUID: supplied}).Uuid; got != supplied {
		t.Fatalf("uuid = %q, want the supplied %q", got, supplied)
	}

	// The SDK replaces an invalid UUID, so the adapter must too, or its
	// response would name a UUID that was never sent.
	for _, supplied := range []string{"", "not-a-uuid"} {
		generated := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", UUID: supplied}).Uuid
		parsed, err := uuid.Parse(generated)
		if err != nil {
			t.Fatalf("uuid for %q = %q, which does not parse: %v", supplied, generated, err)
		}
		if parsed.Version() != 7 {
			t.Fatalf("uuid for %q has version %d, want 7", supplied, parsed.Version())
		}
	}
}

func TestBuildCapturePassesOptionsUnchanged(t *testing.T) {
	options := posthog.Options{"process_person_profile": "no", "future_option": map[string]interface{}{"nested": true}}
	got := buildCapture(CaptureRequest{DistinctID: "u", Event: "e", Options: options}).Options
	if !reflect.DeepEqual(got, options) {
		t.Fatalf("options = %v, want %v", got, options)
	}
}

func TestCompressionFromEnv(t *testing.T) {
	cases := []struct {
		env    string
		want   string
		wantOk bool
	}{
		{"", "gzip", true},
		{"gzip", "gzip", true},
		{"deflate", "deflate", true},
		{"br", "br", true},
		{"zstd", "zstd", true},
		{"brotli", "brotli", false},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("COMPRESSION", tc.env)
			got, ok := compressionFromEnv()
			if got != tc.want || ok != tc.wantOk {
				t.Fatalf("compressionFromEnv() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
}

func TestPadPropertiesMakesCompactJSONExactlySizeBytes(t *testing.T) {
	cases := []struct {
		name       string
		properties map[string]interface{}
		size       int
		marker     string
	}{
		{"no properties", nil, 200, ""},
		{"with properties", map[string]interface{}{"case": "under-ceiling"}, 8323072, ""},
		{"marker starts the padding", map[string]interface{}{"a": 1}, 500, "SECRET-MARKER-7f3a"},
		{"non-ASCII and HTML characters count as UTF-8 bytes", map[string]interface{}{"name": "héllo <b>&"}, 300, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			padded, err := padProperties(tc.properties, tc.size, tc.marker)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(padded)
			if err != nil {
				t.Fatal(err)
			}
			// json.Marshal escapes <, > and & as 6-byte sequences, so measure
			// the unescaped form the harness reads back.
			got, err := compactJSONSize(padded)
			if err != nil || got != tc.size {
				t.Fatalf("compact JSON size = %d (err %v), want %d; marshal gave %d bytes", got, err, tc.size, len(encoded))
			}
			padding, ok := padded[paddingKey].(string)
			if !ok || !strings.HasPrefix(padding, tc.marker) {
				t.Fatalf("padding %q does not start with marker %q", padding, tc.marker)
			}
			for k, v := range tc.properties {
				if !reflect.DeepEqual(padded[k], v) {
					t.Fatalf("property %q = %v, want %v", k, padded[k], v)
				}
			}
		})
	}
}

func TestPadPropertiesDoesNotMutateInputAndRejectsTooSmallSize(t *testing.T) {
	input := map[string]interface{}{"a": "b"}
	if _, err := padProperties(input, 100, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := input[paddingKey]; ok || len(input) != 1 {
		t.Fatalf("input was modified: %v", input)
	}
	if _, err := padProperties(input, 10, "marker"); err == nil {
		t.Fatal("expected an error when the size cannot hold the properties and marker")
	}
}

func TestDropFromFailure(t *testing.T) {
	sent := posthog.CaptureInApi{Uuid: "uuid-1", Event: "test_event"}
	cases := []struct {
		name string
		msg  posthog.APIMessage
		err  error
		want ReportedDrop
	}{
		{
			"capture drop uses the details tag",
			sent,
			&posthog.CaptureEventError{EventUUID: "uuid-1", Result: "drop", Details: "invalid_options"},
			ReportedDrop{UUID: "uuid-1", Event: "test_event", Details: "invalid_options"},
		},
		{
			"duplicate copy",
			&sent,
			&posthog.CaptureEventError{EventUUID: "uuid-1", Result: "drop", Details: "duplicate_event_uuid"},
			ReportedDrop{UUID: "uuid-1", Event: "test_event", Details: "duplicate_event_uuid"},
		},
		{
			"drop without details falls back to the error text",
			sent,
			&posthog.CaptureEventError{EventUUID: "uuid-1", Result: "drop"},
			ReportedDrop{UUID: "uuid-1", Event: "test_event", Details: "capture event uuid-1: drop"},
		},
		{
			"local drop uses the SDK error text",
			sent,
			&posthog.CaptureLocalError{Endpoint: "/i/v1/ai/events", Err: posthog.ErrMessageTooBig},
			ReportedDrop{UUID: "uuid-1", Event: "test_event", Details: (&posthog.CaptureLocalError{Endpoint: "/i/v1/ai/events", Err: posthog.ErrMessageTooBig}).Error()},
		},
		{
			"unknown message type keeps only the details",
			posthog.Identify{},
			errors.New("boom"),
			ReportedDrop{Details: "boom"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dropFromFailure(tc.msg, tc.err); got != tc.want {
				t.Fatalf("dropFromFailure = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStateCallbackAndLoggerFeedState(t *testing.T) {
	s := &AdapterState{pendingEvents: 1}
	stateCallback{state: s}.Failure(
		posthog.CaptureInApi{Uuid: "u", Event: "e"},
		&posthog.CaptureLocalError{Endpoint: "/x", Err: posthog.ErrMessageTooBig},
	)
	if len(s.reportedDrops) != 1 || s.reportedDrops[0].UUID != "u" {
		t.Fatalf("reportedDrops = %+v", s.reportedDrops)
	}
	if s.pendingEvents != 0 {
		t.Fatalf("a local drop must clear its pending event, got %d", s.pendingEvents)
	}

	// A capture-endpoint drop is already counted by the transport.
	s.pendingEvents = 1
	stateCallback{state: s}.Failure(posthog.CaptureInApi{}, &posthog.CaptureEventError{Result: "drop"})
	if s.pendingEvents != 1 {
		t.Fatalf("a capture drop must not change pendingEvents, got %d", s.pendingEvents)
	}

	logger := newStateLogger(s)
	logger.Warnf("event %s dropped", "$ai_trace")
	logger.Errorf("bad %d", 7)
	want := []string{"WARN event $ai_trace dropped", "ERROR bad 7"}
	if !reflect.DeepEqual(s.logLines, want) {
		t.Fatalf("logLines = %v, want %v", s.logLines, want)
	}

	s.clearDiagnostics()
	if len(s.reportedDrops) != 0 || len(s.logLines) != 0 {
		t.Fatal("clearDiagnostics left state behind")
	}
}

func TestSetPropertiesHookSetsKeysOnCaptureOnly(t *testing.T) {
	hook := setPropertiesHook(map[string]interface{}{"layer": "hook"})

	got := hook(posthog.Capture{Event: "e", Properties: posthog.Properties{"layer": "caller", "keep": 1}}).(posthog.Capture)
	if got.Properties["layer"] != "hook" || got.Properties["keep"] != 1 {
		t.Fatalf("properties = %v", got.Properties)
	}
	got = hook(posthog.Capture{Event: "e"}).(posthog.Capture)
	if got.Properties["layer"] != "hook" {
		t.Fatalf("properties = %v", got.Properties)
	}
	if _, ok := hook(posthog.Identify{DistinctId: "u"}).(posthog.Identify); !ok {
		t.Fatal("non-capture message must pass through")
	}
}

func TestHealthDeclaresDiagnosticCapabilities(t *testing.T) {
	recorder := httptest.NewRecorder()
	healthHandler(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health HealthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range health.Capabilities {
		have[c] = true
	}
	for _, want := range []string{"reported_drops", "log_capture", "generated_payloads", "property_layers", "fail_open"} {
		if !have[want] {
			t.Errorf("capability %q missing from %v", want, health.Capabilities)
		}
	}
}
