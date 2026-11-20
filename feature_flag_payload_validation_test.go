package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	json "github.com/goccy/go-json"
)

func TestDecodePayload(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "object", input: `"{\"test\": 1}"`, expected: `{"test": 1}`},
		{name: "array", input: `"[1,false]"`, expected: `[1,false]`},
		{name: "quoted string", input: `"\"hello\""`, expected: `"hello"`},
		{name: "boolean", input: `"false"`, expected: `false`},
		{name: "number", input: `"0"`, expected: `0`},
		{name: "unserialized object", input: `{"color":"green"}`, expected: `{"color":"green"}`},
		{name: "json null", input: `null`, expected: ``},
		{name: "absent", input: ``, expected: ``},
		{name: "malformed", input: `"{broken"`, wantErr: true},
		{name: "whitespace only", input: `"   "`, wantErr: true},
		{name: "bare word", input: `"hello"`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := decodePayload(json.RawMessage(test.input))
			if test.wantErr {
				if err == nil {
					t.Errorf("decodePayload(%s) = %q, expected an error", test.input, payload)
				}
				if payload != "" {
					t.Errorf("decodePayload(%s) returned %q, expected no payload", test.input, payload)
				}
				return
			}
			if err != nil {
				t.Errorf("decodePayload(%s) returned error: %s", test.input, err)
			}
			if payload != test.expected {
				t.Errorf("decodePayload(%s) = %q, expected %q", test.input, payload, test.expected)
			}
		})
	}
}

// warningRecorder collects the warnings a client logs so tests can assert that
// a dropped payload is reported through SDK diagnostics.
type warningRecorder struct {
	mu       sync.Mutex
	warnings []string
}

func (w *warningRecorder) Debugf(format string, args ...interface{}) {}
func (w *warningRecorder) Logf(format string, args ...interface{})   {}
func (w *warningRecorder) Errorf(format string, args ...interface{}) {}

func (w *warningRecorder) Warnf(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, format)
}

func (w *warningRecorder) warnedAbout(substring string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, warning := range w.warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

const malformedPayloadDefinitions = `{
	"count": 1,
	"flags": [
		{
			"id": 1,
			"key": "malformed-payload-flag",
			"active": true,
			"is_simple_flag": true,
			"filters": {
				"groups": [{"properties": [], "rollout_percentage": null}],
				"payloads": {"true": "{broken"}
			}
		},
		{
			"id": 2,
			"key": "valid-payload-flag",
			"active": true,
			"is_simple_flag": true,
			"filters": {
				"groups": [{"properties": [], "rollout_percentage": null}],
				"payloads": {"true": "{\"test\": 1}"}
			}
		}
	]
}`

// A locally evaluated flag whose stored payload is not valid JSON reads as a
// flag with no payload, rather than handing the caller a string it cannot
// decode.
func TestGetFeatureFlagPayloadMalformedLocalDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
			t.Error("expected local evaluation, not a remote flags request")
			http.Error(w, "unexpected remote evaluation", http.StatusBadRequest)
			return
		}
		w.Write([]byte(malformedPayloadDefinitions))
	}))
	defer server.Close()

	logger := &warningRecorder{}
	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		PersonalApiKey: "some very secret key",
		Endpoint:       server.URL,
		Logger:         logger,
	})
	defer client.Close()

	payload, err := client.GetFeatureFlagPayload(FeatureFlagPayload{
		Key:        "malformed-payload-flag",
		DistinctId: "hey",
	})
	if err != nil {
		t.Errorf("expected no error, got %s", err)
	}
	if payload != "" {
		t.Errorf("expected no payload, got %q", payload)
	}
	if !logger.warnedAbout("Ignoring payload for flag") {
		t.Error("expected the dropped payload to be logged")
	}

	result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
		Key:        "malformed-payload-flag",
		DistinctId: "hey",
	})
	if err != nil {
		t.Errorf("expected no error, got %s", err)
	}
	if result.RawPayload != nil {
		t.Errorf("expected no raw payload on the result, got %q", *result.RawPayload)
	}
	if !result.Enabled {
		t.Error("expected a malformed payload to leave the flag value intact")
	}

	// A healthy sibling payload is unaffected.
	valid, err := client.GetFeatureFlagPayload(FeatureFlagPayload{
		Key:        "valid-payload-flag",
		DistinctId: "hey",
	})
	if err != nil {
		t.Errorf("expected no error, got %s", err)
	}
	if valid != `{"test": 1}` {
		t.Errorf("expected the valid payload, got %q", valid)
	}
}

// The same holds for a payload that arrives from remote evaluation.
func TestGetFeatureFlagPayloadMalformedRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
			w.Write([]byte(`{
				"featureFlags": {"enabled-flag": true, "other-flag": true},
				"featureFlagPayloads": {"enabled-flag": "{broken", "other-flag": "{\"test\": 1}"}
			}`))
			return
		}
		t.Errorf("client called an endpoint it shouldn't have: %s", r.URL.Path)
	}))
	defer server.Close()

	logger := &warningRecorder{}
	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint: server.URL,
		Logger:   logger,
	})
	defer client.Close()

	payload, err := client.GetFeatureFlagPayload(FeatureFlagPayload{
		Key:        "enabled-flag",
		DistinctId: "test-user",
	})
	if err != nil {
		t.Errorf("expected no error, got %s", err)
	}
	if payload != "" {
		t.Errorf("expected no payload, got %q", payload)
	}
	if !logger.warnedAbout("Ignoring payload for flag") {
		t.Error("expected the dropped payload to be logged")
	}

	valid, err := client.GetFeatureFlagPayload(FeatureFlagPayload{
		Key:        "other-flag",
		DistinctId: "test-user",
	})
	if err != nil {
		t.Errorf("expected no error, got %s", err)
	}
	if valid != `{"test": 1}` {
		t.Errorf("expected the valid payload, got %q", valid)
	}
}
