package posthog

import (
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

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "non-existent-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "Internal Server Error"}`))
		}))
		defer server.Close()

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, _ = client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "missing-flag",
			DistinctId: "user-123",
		})

		event := client.GetLastCapturedEvent()
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

	t.Run("$feature_flag_errored is true when errors exist", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client, _ := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
		defer client.Close()

		_, err := client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user-123",
		})

		if err == nil {
			t.Error("Expected an error from GetFeatureFlag")
		}

		event := client.GetLastCapturedEvent()
		if event == nil {
			t.Fatal("Expected a captured event")
		}
		if event.Properties["$feature_flag_errored"] != true {
			t.Errorf("Expected $feature_flag_errored to be true, got: %v", event.Properties["$feature_flag_errored"])
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
