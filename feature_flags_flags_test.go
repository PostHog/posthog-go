package posthog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"testing"
)

// eventCapture is a test helper that captures events via the Callback interface
type eventCapture struct {
	mu     sync.Mutex
	events []CaptureInApi
}

func (e *eventCapture) Success(m APIMessage) {
	if capture, ok := m.(CaptureInApi); ok {
		e.mu.Lock()
		e.events = append(e.events, capture)
		e.mu.Unlock()
	}
}

func (e *eventCapture) Failure(m APIMessage, err error) {}

func (e *eventCapture) getLastEvent() *CaptureInApi {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) == 0 {
		return nil
	}
	return &e.events[len(e.events)-1]
}

func (e *eventCapture) waitForEvent(timeout time.Duration) *CaptureInApi {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if event := e.getLastEvent(); event != nil {
			return event
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

/*
Tests of the feature flag api against the flags endpoint, no local evaluation.
This is primarily here to ensure we handle the different versions of the flags
endpoint correctly.
*/
func TestFlags(t *testing.T) {
	t.Parallel()
	validateCapturedEvent := func(t *testing.T, event *CaptureInApi) {
		if event == nil {
			return
		}
		if event.Event != "$feature_flag_called" {
			t.Errorf("Expected a $feature_flag_called event, got: %v", event.Event)
		}

		if event.Properties["$feature_flag_request_id"] != "42853c54-1431-4861-996e-3a548989fa2c" {
			t.Errorf("Expected $feature_flag_request_id property to be 42853c54-1431-4861-996e-3a548989fa2c, got: %v", event.Properties["$feature_flag_request_id"])
		}

		if event.Properties["$feature_flag_evaluated_at"] != int64(1737312368000) {
			t.Errorf("Expected $feature_flag_evaluated_at property to be 1737312368000, got: %v", event.Properties["$feature_flag_evaluated_at"])
		}
	}

	tests := []struct {
		name    string
		fixture string
	}{
		{name: "v3", fixture: "test-flags-v3.json"},
		{name: "v4", fixture: "test-flags-v4.json"},
	}

	for _, test := range tests {
		test := test // Capture loop variable for Go 1.21 compatibility
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/flags") {
				w.Write([]byte(fixture(test.fixture)))
			}
		}))
		defer server.Close()

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "IsFeatureEnabled", flagKey: "enabled-flag", expected: true},
				{name: "IsFeatureEnabled", flagKey: "disabled-flag", expected: false},
				{name: "IsFeatureEnabled", flagKey: "non-existent-flag", expected: false}, // Note: This differs from posthog-node which returns undefined for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					capture := &eventCapture{}
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint:  server.URL,
						BatchSize: 1, // Send immediately
						Callback:  capture,
					})
					defer client.Close()

					isMatch, _ := client.IsFeatureEnabled(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if isMatch != subTest.expected {
						t.Errorf("Expected IsFeatureEnabled to return %v but was %v", subTest.expected, isMatch)
					}

					event := capture.waitForEvent(time.Second)
					validateCapturedEvent(t, event)
				})
			}
		})

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "GetFeatureFlag", flagKey: "enabled-flag", expected: true},
				{name: "GetFeatureFlag", flagKey: "disabled-flag", expected: false},
				{name: "GetFeatureFlag", flagKey: "multi-variate-flag", expected: "hello"},
				{name: "GetFeatureFlag", flagKey: "non-existent-flag", expected: false}, // Note: This differs from posthog-node which returns undefined for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					capture := &eventCapture{}
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint:  server.URL,
						BatchSize: 1, // Send immediately
						Callback:  capture,
					})
					defer client.Close()

					flag, _ := client.GetFeatureFlag(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if flag != subTest.expected {
						t.Errorf("Expected GetFeatureFlag to return %v but was %v", subTest.expected, flag)
					}

					event := capture.waitForEvent(time.Second)
					validateCapturedEvent(t, event)
				})
			}
		})

		t.Run(test.name, func(t *testing.T) {
			subTests := []struct {
				name     string
				flagKey  string
				expected interface{}
			}{
				{name: "GetFeatureFlagPayload", flagKey: "enabled-flag", expected: "{\"foo\": 1}"},
				{name: "GetFeatureFlagPayload", flagKey: "disabled-flag", expected: ""}, // Incorrectly returns "" for disabled flags
				{name: "GetFeatureFlagPayload", flagKey: "multi-variate-flag", expected: "this is the payload"},
				{name: "GetFeatureFlagPayload", flagKey: "non-existent-flag", expected: ""}, // Incorrectly returns "" for non-existent flags
			}

			for _, subTest := range subTests {
				t.Run(subTest.name+" "+subTest.flagKey, func(t *testing.T) {
					capture := &eventCapture{}
					client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
						Endpoint:  server.URL,
						BatchSize: 1, // Send immediately
						Callback:  capture,
					})
					defer client.Close()

					payload, _ := client.GetFeatureFlagPayload(
						FeatureFlagPayload{
							Key:              subTest.flagKey,
							DistinctId:       "some-distinct-id",
							PersonProperties: NewProperties().Set("region", "USA"),
						},
					)

					if payload != subTest.expected {
						t.Errorf("Expected GetFeatureFlagPayload to return %v but was %v", subTest.expected, payload)
					}

					event := capture.waitForEvent(time.Second)
					validateCapturedEvent(t, event)
				})
			}
		})
	}
}

func TestFeatureFlagErrorOnCapturedEvents(t *testing.T) {
	t.Run("success - no $feature_flag_error property", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"test-flag": {"key": "test-flag", "enabled": true, "variant": null}
				},
				"requestId": "req-123",
				"errorsWhileComputingFlags": false
			}`))
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		if _, exists := event.Properties["$feature_flag_error"]; exists {
			t.Errorf("Expected no $feature_flag_error property on success, got: %v", event.Properties["$feature_flag_error"])
		}
	})

	t.Run("errorsWhileComputingFlags sets $feature_flag_error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"test-flag": {"key": "test-flag", "enabled": true, "variant": null}
				},
				"requestId": "req-123",
				"errorsWhileComputingFlags": true
			}`))
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		errorProp := event.Properties["$feature_flag_error"]
		if errorProp != "errors_while_computing_flags" {
			t.Errorf("Expected $feature_flag_error to be 'errors_while_computing_flags', got: %v", errorProp)
		}
	})

	t.Run("quota limited sets $feature_flag_error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {},
				"requestId": "req-123",
				"quota_limited": ["feature_flags"]
			}`))
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		errorProp := event.Properties["$feature_flag_error"]
		if errorProp != "quota_limited" {
			t.Errorf("Expected $feature_flag_error to be 'quota_limited', got: %v", errorProp)
		}
	})

	t.Run("missing flag sets $feature_flag_error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"other-flag": {"key": "other-flag", "enabled": true, "variant": null}
				},
				"requestId": "req-123"
			}`))
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "non-existent-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		errorProp := event.Properties["$feature_flag_error"]
		if errorProp != "flag_missing" {
			t.Errorf("Expected $feature_flag_error to be 'flag_missing', got: %v", errorProp)
		}
	})

	t.Run("API error sets $feature_flag_error with status code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/flags") {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": "Internal Server Error"}`))
			}
			// /batch/ returns 200 OK (default) to allow event capture
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		errorProp := event.Properties["$feature_flag_error"]
		if errorProp != "api_error_500" {
			t.Errorf("Expected $feature_flag_error to be 'api_error_500', got: %v", errorProp)
		}
	})

	t.Run("multiple errors are comma-separated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {},
				"requestId": "req-123",
				"errorsWhileComputingFlags": true
			}`))
		}))
		defer server.Close()

		capture := &eventCapture{}
		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL, Callback: capture, BatchSize: 1})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "missing-flag",
			DistinctId: "user-123",
		})

		event := capture.waitForEvent(time.Second)
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		errorProp, ok := event.Properties["$feature_flag_error"].(string)
		if !ok {
			t.Fatalf("Expected $feature_flag_error to be a string, got: %T", event.Properties["$feature_flag_error"])
		}
		// Should contain both errors (order: errorsWhileComputingFlags, then flag_missing)
		if !strings.Contains(errorProp, "errors_while_computing_flags") {
			t.Errorf("Expected $feature_flag_error to contain 'errors_while_computing_flags', got: %v", errorProp)
		}
		if !strings.Contains(errorProp, "flag_missing") {
			t.Errorf("Expected $feature_flag_error to contain 'flag_missing', got: %v", errorProp)
		}
	})
}

func TestFlagsV4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-flags-v4.json")))
		}
	}))
	defer server.Close()

	tests := []struct {
		name            string
		flagKey         string
		expected        interface{}
		expectedVersion int
		expectedReason  string
		expectedId      int
	}{
		{name: "GetFeatureFlag", flagKey: "enabled-flag", expected: true, expectedVersion: 23, expectedReason: "Matched conditions set 3", expectedId: 1},
		{name: "GetFeatureFlag", flagKey: "disabled-flag", expected: false, expectedVersion: 12, expectedReason: "No matching condition set", expectedId: 3},
		{name: "GetFeatureFlag", flagKey: "multi-variate-flag", expected: "hello", expectedVersion: 42, expectedReason: "Matched conditions set 2", expectedId: 4},
	}

	for _, test := range tests {
		t.Run(test.name+" "+test.flagKey, func(t *testing.T) {
			capture := &eventCapture{}
			client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
				Endpoint:  server.URL,
				BatchSize: 1, // Send immediately
				Callback:  capture,
			})
			defer client.Close()

			flag, _ := client.GetFeatureFlag(
				FeatureFlagPayload{
					Key:        test.flagKey,
					DistinctId: "some-distinct-id",
				},
			)

			if flag != test.expected {
				t.Errorf("Expected GetFeatureFlag(%s) to return %v but was %v", test.flagKey, test.expected, flag)
			}

			capturedEvent := capture.waitForEvent(time.Second)
			if capturedEvent == nil {
				t.Fatalf("Expected a $feature_flag_called for %s event, got nil", test.flagKey)
			}

			if capturedEvent.Properties["$feature_flag"] != test.flagKey {
				t.Errorf("Expected $feature_flag property to be %v, got: %v", test.flagKey, capturedEvent.Properties["$feature_flag"])
			}

			if capturedEvent.Properties["$feature_flag_request_id"] != "42853c54-1431-4861-996e-3a548989fa2c" {
				t.Errorf("Expected $feature_flag_request_id property to be 42853c54-1431-4861-996e-3a548989fa2c, got: %v", capturedEvent.Properties["$feature_flag_request_id"])
			}

			if capturedEvent.Properties["$feature_flag_response"] != test.expected {
				t.Errorf("Expected $feature_flag_response property for %s to be %v, got: %v", test.flagKey, test.expected, capturedEvent.Properties["$feature_flag_response"])
			}

			if version, ok := capturedEvent.Properties["$feature_flag_version"].(int); !ok || version != test.expectedVersion {
				t.Errorf("Expected $feature_flag_version property for %s to be %v, got: %v", test.flagKey, test.expectedVersion, capturedEvent.Properties["$feature_flag_version"])
			}

			if reason, ok := capturedEvent.Properties["$feature_flag_reason"].(string); !ok || reason != test.expectedReason {
				t.Errorf("Expected $feature_flag_reason property for %s to be %v, got: %v", test.flagKey, test.expectedReason, capturedEvent.Properties["$feature_flag_reason"])
			}

			if id, ok := capturedEvent.Properties["$feature_flag_id"].(int); !ok || int(id) != test.expectedId {
				t.Errorf("Expected $feature_flag_id for %s to be %v, got: %v", test.flagKey, test.expectedId, capturedEvent.Properties["$feature_flag_id"])
			}
		})
	}
}

func TestGetFeatureFlagResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fixture    string
		apiVersion string
	}{
		{name: "v3", fixture: "test-flags-v3.json", apiVersion: "v3"},
		{name: "v4", fixture: "test-flags-v4.json", apiVersion: "v4"},
	}

	for _, test := range tests {
		test := test
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/flags") {
				w.Write([]byte(fixture(test.fixture)))
			}
		}))
		defer server.Close()

		t.Run(test.name+" returns value and payload for boolean flag", func(t *testing.T) {
			capture := &eventCapture{}
			client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				Callback:  capture,
			})
			defer client.Close()

			result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
				Key:        "enabled-flag",
				DistinctId: "some-distinct-id",
			})

			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			if result.Key != "enabled-flag" {
				t.Errorf("Expected Key to be 'enabled-flag', got: %v", result.Key)
			}

			if !result.Enabled {
				t.Error("Expected Enabled to be true")
			}

			if result.RawPayload == nil || *result.RawPayload != "{\"foo\": 1}" {
				t.Errorf("Expected RawPayload to be '{\"foo\": 1}', got: %v", result.RawPayload)
			}

			if result.Variant != nil {
				t.Errorf("Expected Variant to be nil for boolean flag, got: %v", result.Variant)
			}

			// Verify $feature_flag_called event was emitted
			event := capture.waitForEvent(time.Second)
			if event == nil {
				t.Fatal("Expected a $feature_flag_called event")
			}

			if event.Event != "$feature_flag_called" {
				t.Errorf("Expected event to be '$feature_flag_called', got: %v", event.Event)
			}

			if event.Properties["$feature_flag"] != "enabled-flag" {
				t.Errorf("Expected $feature_flag to be 'enabled-flag', got: %v", event.Properties["$feature_flag"])
			}

			if event.Properties["$feature_flag_response"] != true {
				t.Errorf("Expected $feature_flag_response to be true, got: %v", event.Properties["$feature_flag_response"])
			}
		})

		t.Run(test.name+" returns value and payload for multivariate flag", func(t *testing.T) {
			capture := &eventCapture{}
			client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				Callback:  capture,
			})
			defer client.Close()

			result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
				Key:        "multi-variate-flag",
				DistinctId: "some-distinct-id",
			})

			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			if result.Key != "multi-variate-flag" {
				t.Errorf("Expected Key to be 'multi-variate-flag', got: %v", result.Key)
			}

			if !result.Enabled {
				t.Error("Expected Enabled to be true for multivariate flag")
			}

			if result.RawPayload == nil || *result.RawPayload != "this is the payload" {
				t.Errorf("Expected RawPayload to be 'this is the payload', got: %v", result.RawPayload)
			}

			if result.Variant == nil || *result.Variant != "hello" {
				t.Errorf("Expected Variant to be 'hello', got: %v", result.Variant)
			}

			// Verify $feature_flag_called event was emitted
			event := capture.waitForEvent(time.Second)
			if event == nil {
				t.Fatal("Expected a $feature_flag_called event")
			}

			if event.Properties["$feature_flag_response"] != "hello" {
				t.Errorf("Expected $feature_flag_response to be 'hello', got: %v", event.Properties["$feature_flag_response"])
			}
		})

		t.Run(test.name+" returns disabled flag correctly", func(t *testing.T) {
			capture := &eventCapture{}
			client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				Callback:  capture,
			})
			defer client.Close()

			result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
				Key:        "disabled-flag",
				DistinctId: "some-distinct-id",
			})

			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			if result.Enabled {
				t.Error("Expected Enabled to be false for disabled flag")
			}

			if result.RawPayload != nil {
				t.Errorf("Expected RawPayload to be nil for disabled flag, got: %v", result.RawPayload)
			}

			// Verify $feature_flag_called event was emitted
			event := capture.waitForEvent(time.Second)
			if event == nil {
				t.Fatal("Expected a $feature_flag_called event")
			}
		})

		t.Run(test.name+" returns non-existent flag correctly", func(t *testing.T) {
			capture := &eventCapture{}
			client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				Callback:  capture,
			})
			defer client.Close()

			result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
				Key:        "non-existent-flag",
				DistinctId: "some-distinct-id",
			})

			if err == nil {
				t.Fatal("Expected an error for non-existent flag")
			}
			if !errors.Is(err, ErrFlagNotFound) {
				t.Errorf("Expected ErrFlagNotFound, got: %v", err)
			}
			if result != nil {
				t.Errorf("Expected nil result for non-existent flag, got: %v", result)
			}

			// Verify $feature_flag_called event was emitted with error
			event := capture.waitForEvent(time.Second)
			if event == nil {
				t.Fatal("Expected a $feature_flag_called event")
			}

			if event.Properties["$feature_flag_error"] != "flag_missing" {
				t.Errorf("Expected $feature_flag_error to be 'flag_missing', got: %v", event.Properties["$feature_flag_error"])
			}
		})
	}
}

func TestGetFeatureFlagResultGetPayloadAs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(fixture("test-flags-v4.json")))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
	})
	defer client.Close()

	t.Run("unmarshals JSON payload correctly", func(t *testing.T) {
		result, _ := client.GetFeatureFlagResult(FeatureFlagPayload{
			Key:        "enabled-flag",
			DistinctId: "some-distinct-id",
		})

		var payload struct {
			Foo int `json:"foo"`
		}
		err := result.GetPayloadAs(&payload)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if payload.Foo != 1 {
			t.Errorf("Expected Foo to be 1, got: %v", payload.Foo)
		}
	})

	t.Run("returns error for empty payload", func(t *testing.T) {
		result, _ := client.GetFeatureFlagResult(FeatureFlagPayload{
			Key:        "disabled-flag",
			DistinctId: "another-distinct-id",
		})

		var payload struct {
			Foo int `json:"foo"`
		}
		err := result.GetPayloadAs(&payload)
		if err == nil {
			t.Error("Expected error for empty payload")
		}
	})
}

func TestGetFeatureFlagResultReturnsErrorForNonExistentFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			// Return empty flags - the requested flag won't exist
			w.Write([]byte(`{"flags": [], "group_type_mapping": {}}`))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "some-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
		Key:                 "non-existent-flag",
		DistinctId:          "some-distinct-id",
		OnlyEvaluateLocally: true,
	})

	if err == nil {
		t.Fatal("Expected an error for non-existent flag with OnlyEvaluateLocally")
	}

	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Expected error message to contain 'does not exist', got: %v", err)
	}

	if result != nil {
		t.Errorf("Expected result to be nil, got: %v", result)
	}
}

func TestGetFeatureFlagResultPropagatesLocalEvaluationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey:            "some-personal-api-key",
		Endpoint:                  server.URL,
		FeatureFlagRequestTimeout: 10 * time.Millisecond,
	})
	defer client.Close()

	result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
		Key:                 "any-flag",
		DistinctId:          "some-distinct-id",
		OnlyEvaluateLocally: true,
	})

	if err == nil {
		t.Fatal("Expected an error when flags fail to fetch")
	}

	// Should get the original evaluation error, not ErrFlagNotFound
	if err.Error() != "flags were not successfully fetched yet" {
		t.Errorf("Expected error 'flags were not successfully fetched yet', got: %v", err)
	}

	if errors.Is(err, ErrFlagNotFound) {
		t.Error("Error should NOT be ErrFlagNotFound - it's an evaluation error")
	}

	if result != nil {
		t.Errorf("Expected result to be nil, got: %v", result)
	}
}

func TestGetFeatureFlagResultPropagatesRemoteAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "Internal Server Error"}`))
		}
	}))
	defer server.Close()

	// No PersonalApiKey = uses remote /flags path
	client, _ := NewWithConfig("test-api-key", Config{
		Endpoint: server.URL,
	})
	defer client.Close()

	result, err := client.GetFeatureFlagResult(FeatureFlagPayload{
		Key:        "any-flag",
		DistinctId: "some-distinct-id",
	})

	if err == nil {
		t.Fatal("Expected an error when /flags returns 500")
	}

	// API errors should be propagated, not wrapped with ErrFlagNotFound
	if errors.Is(err, ErrFlagNotFound) {
		t.Error("Error should NOT be ErrFlagNotFound - it's an API error")
	}

	if result != nil {
		t.Errorf("Expected result to be nil, got: %v", result)
	}
}
