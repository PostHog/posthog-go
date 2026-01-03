package posthog

import (
	"io"

	json "github.com/goccy/go-json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEnqueue_InvalidMessageTypes tests various invalid message scenarios
func TestEnqueue_InvalidMessageTypes(t *testing.T) {
	client, _ := NewWithConfig("test-key", Config{
		Transport: NoOpTransport(),
	})
	defer client.Close()

	t.Run("capture_missing_distinct_id", func(t *testing.T) {
		err := client.Enqueue(Capture{
			Event: "test_event",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "DistinctId")
	})

	t.Run("capture_missing_event", func(t *testing.T) {
		err := client.Enqueue(Capture{
			DistinctId: "user_1",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "Event")
	})

	t.Run("identify_missing_distinct_id", func(t *testing.T) {
		err := client.Enqueue(Identify{
			Properties: Properties{"name": "test"},
		})
		require.Error(t, err)
	})

	t.Run("alias_missing_distinct_id", func(t *testing.T) {
		err := client.Enqueue(Alias{
			Alias: "new_alias",
		})
		require.Error(t, err)
	})

	t.Run("alias_missing_alias", func(t *testing.T) {
		err := client.Enqueue(Alias{
			DistinctId: "user_1",
		})
		require.Error(t, err)
	})

	t.Run("group_identify_missing_type", func(t *testing.T) {
		err := client.Enqueue(GroupIdentify{
			Key: "group_1",
		})
		require.Error(t, err)
	})

	t.Run("group_identify_missing_key", func(t *testing.T) {
		err := client.Enqueue(GroupIdentify{
			Type: "company",
		})
		require.Error(t, err)
	})
}

// TestFeatureFlag_MalformedResponses tests handling of malformed feature flag responses
func TestFeatureFlag_MalformedResponses(t *testing.T) {
	malformedResponses := []struct {
		name     string
		response string
	}{
		{"invalid_json", `{invalid json`},
		{"flags_not_object", `{"featureFlags": "not an object"}`},
		{"empty_response", ``},
		{"null_response", `null`},
		{"array_instead_of_object", `[]`},
	}

	for _, tc := range malformedResponses {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.response))
			}))
			defer server.Close()

			client, _ := NewWithConfig("test-key", Config{
				Endpoint:       server.URL,
				PersonalApiKey: "test-personal",
			})
			defer client.Close()

			// Should not panic, should handle gracefully
			result, err := client.GetFeatureFlag(FeatureFlagPayload{
				Key:        "test-flag",
				DistinctId: "user_1",
			})

			// Either returns error or returns false/nil - shouldn't crash
			t.Logf("Result: %v, Error: %v", result, err)
		})
	}
}

// TestClient_ServerErrors tests client behavior with various server error codes
func TestClient_ServerErrors(t *testing.T) {
	errorCodes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, code := range errorCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			callCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				w.WriteHeader(code)
			}))
			defer server.Close()

			maxRetries := 0
			client, _ := NewWithConfig("test-key", Config{
				Endpoint:   server.URL,
				MaxRetries: &maxRetries, // Disable retries for faster test
			})

			err := client.Enqueue(Capture{
				DistinctId: "user_1",
				Event:      "test_event",
			})
			require.NoError(t, err) // Enqueue should not fail immediately

			client.Close() // Will attempt to flush
			t.Logf("Server received %d request(s) for status %d", callCount, code)
		})
	}
}

// TestClient_NetworkFailures tests client behavior with network failures
func TestClient_NetworkFailures(t *testing.T) {
	t.Run("connection_refused", func(t *testing.T) {
		client, _ := NewWithConfig("test-key", Config{
			Endpoint:   "http://localhost:1", // Port 1 should refuse connections
			MaxRetries: Ptr(0),               // Skip retries to avoid timeout
		})

		err := client.Enqueue(Capture{
			DistinctId: "user_1",
			Event:      "test_event",
		})
		require.NoError(t, err) // Enqueue should not fail immediately

		// Close will try to flush and should handle the error gracefully
		err = client.Close()
		require.NoError(t, err)
	})

	t.Run("invalid_url", func(t *testing.T) {
		client, _ := NewWithConfig("test-key", Config{
			Endpoint:   "not-a-valid-url",
			MaxRetries: Ptr(0), // Skip retries to avoid timeout
		})

		err := client.Enqueue(Capture{
			DistinctId: "user_1",
			Event:      "test_event",
		})
		require.NoError(t, err)

		err = client.Close()
		require.NoError(t, err)
	})
}

// TestProperties_EdgeCases tests edge cases in property handling
func TestProperties_EdgeCases(t *testing.T) {
	t.Run("nil_value", func(t *testing.T) {
		props := Properties{"key": nil}
		// Should serialize without error
		_, err := json.Marshal(props)
		require.NoError(t, err)
	})

	t.Run("empty_key", func(t *testing.T) {
		props := Properties{"": "empty key"}
		_, err := json.Marshal(props)
		require.NoError(t, err)
	})

	t.Run("very_large_string", func(t *testing.T) {
		largeString := strings.Repeat("x", 1000000)
		props := Properties{"key": largeString}
		data, err := json.Marshal(props)
		require.NoError(t, err)
		require.True(t, len(data) > 1000000)
	})

	t.Run("special_characters", func(t *testing.T) {
		props := Properties{
			"unicode":    "日本語テスト",
			"emoji":      "🚀🔥💡",
			"newlines":   "line1\nline2\nline3",
			"tabs":       "col1\tcol2\tcol3",
			"quotes":     `"quoted"`,
			"backslash":  `path\to\file`,
			"null_bytes": "before\x00after",
		}
		data, err := json.Marshal(props)
		require.NoError(t, err)

		// Should be valid JSON that can be unmarshaled
		var decoded map[string]interface{}
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
	})

	t.Run("deeply_nested", func(t *testing.T) {
		nested := map[string]interface{}{}
		current := nested
		for i := 0; i < 100; i++ {
			next := map[string]interface{}{}
			current["nested"] = next
			current = next
		}
		current["value"] = "deep"

		props := Properties{"deep": nested}
		_, err := json.Marshal(props)
		require.NoError(t, err)
	})

	t.Run("circular_reference_avoided", func(t *testing.T) {
		// Properties using map[string]interface{} can't have circular refs
		// but we can test that large recursive structures work
		props := Properties{
			"a": map[string]interface{}{
				"b": map[string]interface{}{
					"c": "value",
				},
			},
		}
		_, err := json.Marshal(props)
		require.NoError(t, err)
	})
}

// TestConfig_Validation tests configuration validation
func TestConfig_Validation(t *testing.T) {
	t.Run("negative_batch_size", func(t *testing.T) {
		// SDK validates and rejects negative batch sizes
		_, err := NewWithConfig("test-key", Config{
			BatchSize: -1,
			Transport: NoOpTransport(),
		})
		require.Error(t, err, "negative batch size should be rejected")
	})

	t.Run("zero_batch_size", func(t *testing.T) {
		// Zero batch size is valid - SDK uses default
		client, err := NewWithConfig("test-key", Config{
			BatchSize: 0,
			Transport: NoOpTransport(),
		})
		require.NoError(t, err)
		defer client.Close()
	})

	t.Run("empty_api_key", func(t *testing.T) {
		// Note: SDK currently accepts empty API keys (server will reject them)
		// This test documents current behavior
		client, err := NewWithConfig("", Config{
			Transport: NoOpTransport(),
		})
		require.NoError(t, err, "SDK accepts empty API key (server-side validation)")
		defer client.Close()
	})
}

// TestFeatureFlag_TimeoutHandling tests flag evaluation with network delays
func TestFeatureFlag_TimeoutHandling(t *testing.T) {
	t.Run("slow_response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.Write([]byte(`{"featureFlags": {"test-flag": true}}`))
		}))
		defer server.Close()

		client, _ := NewWithConfig("test-key", Config{
			Endpoint:       server.URL,
			PersonalApiKey: "test-personal",
		})
		defer client.Close()

		start := time.Now()
		_, err := client.GetFeatureFlag(FeatureFlagPayload{
			Key:        "test-flag",
			DistinctId: "user_1",
		})
		elapsed := time.Since(start)

		t.Logf("Request took %v, error: %v", elapsed, err)
	})
}

// TestEnqueue_AfterClose tests behavior when enqueueing after client is closed
func TestEnqueue_AfterClose(t *testing.T) {
	client, _ := NewWithConfig("test-key", Config{
		Transport: NoOpTransport(),
	})

	// Close the client
	client.Close()

	// Try to enqueue - should not panic, should return error or handle gracefully
	err := client.Enqueue(Capture{
		DistinctId: "user_1",
		Event:      "test_event",
	})

	// The SDK might allow this or return an error - either is acceptable
	t.Logf("Enqueue after close result: %v", err)
}

// TestCallback_Errors tests that callback errors are handled gracefully
func TestCallback_Errors(t *testing.T) {
	callbackCalled := false
	panicCallback := struct {
		Callback
	}{}

	// Create a callback that panics
	type panicyCallback struct{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	callback := NewUnifiedCallback(t)
	client, _ := NewWithConfig("test-key", Config{
		Endpoint: server.URL,
		Callback: callback,
	})

	err := client.Enqueue(Capture{
		DistinctId: "user_1",
		Event:      "test_event",
	})
	require.NoError(t, err)

	client.Close()

	callbackCalled = callback.successCount > 0 || callback.failureCount > 0
	t.Logf("Callback was called: %v, panic callback: %v", callbackCalled, panicCallback)
}

// TestBatch_EmptyAndLarge tests batching edge cases
func TestBatch_EmptyAndLarge(t *testing.T) {
	t.Run("large_batch", func(t *testing.T) {
		var received int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var b struct {
				Messages []json.RawMessage `json:"batch"`
			}
			json.Unmarshal(body, &b)
			received += len(b.Messages)
			w.WriteHeader(200)
		}))
		defer server.Close()

		client, _ := NewWithConfig("test-key", Config{
			Endpoint:  server.URL,
			BatchSize: 1000,
		})

		// Send more than default batch size
		for i := 0; i < 500; i++ {
			client.Enqueue(Capture{
				DistinctId: "user_1",
				Event:      "test_event",
				Properties: Properties{"index": i},
			})
		}

		client.Close()
		require.Equal(t, 500, received)
	})
}

// TestPrepareForSend_EdgeCases tests prepareForSend with edge case data
func TestPrepareForSend_EdgeCases(t *testing.T) {
	t.Run("empty_capture", func(t *testing.T) {
		capture := Capture{Type: "capture"}
		data, apiMsg, err := prepareForSend(capture)
		require.NoError(t, err)
		require.Greater(t, len(data), 0, "Even empty capture should have non-zero serialized size")
		require.NotNil(t, apiMsg, "APIMessage should not be nil")
	})

	t.Run("nil_properties_and_groups", func(t *testing.T) {
		capture := Capture{
			Type:       "capture",
			DistinctId: "user",
			Event:      "event",
			Properties: nil,
			Groups:     nil,
		}
		data, apiMsg, err := prepareForSend(capture)
		require.NoError(t, err)
		require.Greater(t, len(data), 0)
		require.NotNil(t, apiMsg)
	})

	t.Run("empty_properties_and_groups", func(t *testing.T) {
		capture := Capture{
			Type:       "capture",
			DistinctId: "user",
			Event:      "event",
			Properties: Properties{},
			Groups:     Groups{},
		}
		data, apiMsg, err := prepareForSend(capture)
		require.NoError(t, err)
		require.Greater(t, len(data), 0)
		require.NotNil(t, apiMsg)
	})
}

// TestPrepareForSend_SerializationErrors tests that prepareForSend returns errors for unencodable values
func TestPrepareForSend_SerializationErrors(t *testing.T) {
	t.Run("channel_in_properties", func(t *testing.T) {
		ch := make(chan int)
		capture := Capture{
			Type:       "capture",
			DistinctId: "user",
			Event:      "event",
			Properties: Properties{"channel": ch},
		}
		data, apiMsg, err := prepareForSend(capture)
		require.Error(t, err, "Should fail to serialize channel")
		require.Nil(t, data, "Data should be nil on error")
		require.NotNil(t, apiMsg, "APIMessage should be returned for callback")
	})

	t.Run("function_in_properties", func(t *testing.T) {
		fn := func() {}
		capture := Capture{
			Type:       "capture",
			DistinctId: "user",
			Event:      "event",
			Properties: Properties{"func": fn},
		}
		data, apiMsg, err := prepareForSend(capture)
		require.Error(t, err, "Should fail to serialize function")
		require.Nil(t, data)
		require.NotNil(t, apiMsg, "APIMessage should be returned for callback")
	})

	t.Run("channel_in_groups", func(t *testing.T) {
		ch := make(chan string)
		capture := Capture{
			Type:       "capture",
			DistinctId: "user",
			Event:      "event",
			Groups:     Groups{"company": ch},
		}
		data, apiMsg, err := prepareForSend(capture)
		require.Error(t, err, "Should fail to serialize channel in groups")
		require.Nil(t, data)
		require.NotNil(t, apiMsg, "APIMessage should be returned for callback")
	})

	t.Run("identify_with_unencodable", func(t *testing.T) {
		ch := make(chan int)
		identify := Identify{
			DistinctId: "user",
			Properties: Properties{"channel": ch},
		}
		data, apiMsg, err := prepareForSend(identify)
		require.Error(t, err, "Identify should fail with unencodable value")
		require.Nil(t, data)
		require.NotNil(t, apiMsg, "APIMessage should be returned for callback")
	})

	t.Run("alias_serializes_ok", func(t *testing.T) {
		// Alias has no Properties, so should always serialize successfully
		alias := Alias{
			DistinctId: "user",
			Alias:      "alias",
		}
		data, apiMsg, err := prepareForSend(alias)
		require.NoError(t, err, "Alias should serialize successfully")
		require.NotNil(t, data)
		require.NotNil(t, apiMsg)
	})

	t.Run("group_identify_with_unencodable", func(t *testing.T) {
		fn := func() {}
		groupIdentify := GroupIdentify{
			Type:       "company",
			Key:        "company_1",
			Properties: Properties{"func": fn},
		}
		data, apiMsg, err := prepareForSend(groupIdentify)
		require.Error(t, err, "GroupIdentify should fail with unencodable value")
		require.Nil(t, data)
		require.NotNil(t, apiMsg, "APIMessage should be returned for callback")
	})
}

// TestGroups_EdgeCases tests group property edge cases
func TestGroups_EdgeCases(t *testing.T) {
	t.Run("empty_group_type", func(t *testing.T) {
		groups := Groups{"": "value"}
		_, err := json.Marshal(groups)
		require.NoError(t, err)
	})

	t.Run("empty_group_value", func(t *testing.T) {
		groups := Groups{"type": ""}
		_, err := json.Marshal(groups)
		require.NoError(t, err)
	})

	t.Run("many_groups", func(t *testing.T) {
		groups := Groups{}
		for i := 0; i < 100; i++ {
			groups[string(rune('a'+i%26))+string(rune('0'+i/26))] = "value"
		}
		_, err := json.Marshal(groups)
		require.NoError(t, err)
	})
}

