package posthog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Test 1: BeforeSend not configured - message passes through unchanged
func TestBeforeSendNotConfigured(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		Properties:       Properties{"original": "value"},
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	result := <-body

	// Parse the JSON response to verify the message was sent
	var response struct {
		Batch []map[string]interface{} `json:"batch"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(response.Batch) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(response.Batch))
	}

	msg := response.Batch[0]
	properties := msg["properties"].(map[string]interface{})
	if properties["original"] != "value" {
		t.Error("Expected original property to be preserved")
	}
}

// Test 2: BeforeSend modifies message properties
func TestBeforeSendModifiesProperties(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	var receivedMsg Message
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		BeforeSend: func(msg Message) Message {
			receivedMsg = msg
			if capture, ok := msg.(Capture); ok {
				if capture.Properties == nil {
					capture.Properties = NewProperties()
				}
				capture.Properties["modified"] = true
				capture.Properties["hook_added"] = "test_value"
				return capture
			}
			return msg
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		Properties:       Properties{"original": "value"},
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	result := <-body

	// Verify the hook was called
	if receivedMsg == nil {
		t.Fatal("BeforeSend hook was not called")
	}

	// Parse the JSON response to verify the message was modified
	var response struct {
		Batch []map[string]interface{} `json:"batch"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify the message was modified
	if len(response.Batch) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(response.Batch))
	}

	msg := response.Batch[0]
	properties := msg["properties"].(map[string]interface{})
	if properties["modified"] != true {
		t.Error("Expected 'modified' property to be true")
	}
	if properties["hook_added"] != "test_value" {
		t.Error("Expected 'hook_added' property to be 'test_value'")
	}
	if properties["original"] != "value" {
		t.Error("Expected 'original' property to be preserved")
	}
}

// Test 3: BeforeSend modifies DistinctId
func TestBeforeSendModifiesDistinctId(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		BeforeSend: func(msg Message) Message {
			if capture, ok := msg.(Capture); ok {
				capture.DistinctId = "modified_user_id"
				return capture
			}
			return msg
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "original_user_id",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	result := <-body

	// Parse the JSON response to verify DistinctId was modified
	var response struct {
		Batch []map[string]interface{} `json:"batch"`
	}
	json.Unmarshal(result, &response)

	msg := response.Batch[0]
	if msg["distinct_id"] != "modified_user_id" {
		t.Errorf("Expected DistinctId to be 'modified_user_id', got '%s'", msg["distinct_id"])
	}
}

// Test 4: BeforeSend returns nil - event should be dropped
func TestBeforeSendDropsEvent(t *testing.T) {
	eventsSent := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventsSent++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	warningLogged := false
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		Logger: testLogger{
			logf: func(format string, args ...interface{}) {
				msg := fmt.Sprintf(format, args...)
				if strings.Contains(msg, "dropped by BeforeSend") && strings.Contains(msg, "test_event") {
					warningLogged = true
				}
			},
		},
		BeforeSend: func(msg Message) Message {
			// Drop all events
			return nil
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	// Should return nil (success, but not sent)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Wait a bit to ensure no event was sent
	time.Sleep(100 * time.Millisecond)

	if eventsSent > 0 {
		t.Errorf("Expected 0 events to be sent, got %d", eventsSent)
	}

	if !warningLogged {
		t.Error("Expected warning to be logged when event was dropped")
	}
}

// Test 5: BeforeSend panics - should log error and use original message
func TestBeforeSendPanics(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	errorLogged := false
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		Logger: testLogger{
			logf: t.Logf,
			errorf: func(format string, args ...interface{}) {
				msg := fmt.Sprintf(format, args...)
				if strings.Contains(msg, "panic in BeforeSend") {
					errorLogged = true
				}
			},
		},
		BeforeSend: func(msg Message) Message {
			panic("intentional panic for testing")
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Original message should have been sent
	result := <-body

	// Parse the JSON response
	var response struct {
		Batch []map[string]interface{} `json:"batch"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(response.Batch) != 1 {
		t.Fatal("Expected original message to be sent despite panic")
	}

	if !errorLogged {
		t.Error("Expected error to be logged when hook panicked")
	}
}

// Test 6: BeforeSend returns invalid message - should use original
func TestBeforeSendReturnsInvalidMessage(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	errorLogged := false
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		Logger: testLogger{
			logf: t.Logf,
			errorf: func(format string, args ...interface{}) {
				msg := fmt.Sprintf(format, args...)
				if strings.Contains(msg, "validation failed after BeforeSend") {
					errorLogged = true
				}
			},
		},
		BeforeSend: func(msg Message) Message {
			// Return an invalid message (empty DistinctId)
			return Capture{
				Event:      "test_event",
				DistinctId: "", // Invalid - required field
			}
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Original message should have been sent
	result := <-body

	// Parse the JSON response
	var response struct {
		Batch []map[string]interface{} `json:"batch"`
	}
	json.Unmarshal(result, &response)

	msg := response.Batch[0]
	if msg["distinct_id"] != "user123" {
		t.Error("Expected original valid DistinctId to be sent")
	}

	if !errorLogged {
		t.Error("Expected error to be logged for invalid message")
	}
}

// Test 7: BeforeSend works with different message types
func TestBeforeSendWithDifferentMessageTypes(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "Capture",
			msg: Capture{
				Event:            "test_event",
				DistinctId:       "user123",
				SendFeatureFlags: SendFeatureFlags(false),
			},
		},
		{
			name: "Identify",
			msg: Identify{
				DistinctId: "user123",
				Properties: Properties{"name": "Test User"},
			},
		},
		{
			name: "Alias",
			msg: Alias{
				DistinctId: "user123",
				Alias:      "user_alias",
			},
		},
		{
			name: "GroupIdentify",
			msg: GroupIdentify{
				Type:       "company",
				Key:        "company123",
				Properties: Properties{"name": "Test Company"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, server := mockServer()
			defer server.Close()

			hookCalled := false
			client, _ := NewWithConfig("test-key", Config{
				Endpoint:  server.URL,
				BatchSize: 1,
				now:       mockTime,
				BeforeSend: func(msg Message) Message {
					hookCalled = true
					// Verify we can type assert correctly
					switch msg.(type) {
					case Capture, Identify, Alias, GroupIdentify:
						// Expected types
					default:
						t.Errorf("Unexpected message type: %T", msg)
					}
					return msg
				},
			})
			defer client.Close()

			err := client.Enqueue(tt.msg)
			if err != nil {
				t.Fatalf("Enqueue failed: %v", err)
			}

			<-body // Wait for message

			if !hookCalled {
				t.Error("BeforeSend hook was not called")
			}
		})
	}
}

// Test 8: BeforeSend called after enrichment
func TestBeforeSendCalledAfterEnrichment(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	var receivedCapture Capture
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		BeforeSend: func(msg Message) Message {
			if capture, ok := msg.(Capture); ok {
				receivedCapture = capture
			}
			return msg
		},
	})
	defer client.Close()

	// Enqueue without timestamp
	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	<-body // Wait for message

	// Verify timestamp was set before hook was called
	if receivedCapture.Timestamp.IsZero() {
		t.Error("Expected Timestamp to be set before BeforeSend hook")
	}

	// Verify type was set
	if receivedCapture.Type != "capture" {
		t.Errorf("Expected Type to be 'capture', got '%s'", receivedCapture.Type)
	}
}

// Test 9: BeforeSend filters by properties
func TestBeforeSendFiltersByProperties(t *testing.T) {
	eventsSent := 0
	var sentEvents []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the JSON batch
		var response struct {
			Batch []map[string]interface{} `json:"batch"`
		}
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		json.Unmarshal(body, &response)
		eventsSent += len(response.Batch)
		for _, msg := range response.Batch {
			if event, ok := msg["event"].(string); ok {
				sentEvents = append(sentEvents, event)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 10,
		Interval:  10 * time.Millisecond,
		BeforeSend: func(msg Message) Message {
			if capture, ok := msg.(Capture); ok {
				// Drop events with internal=true property
				if capture.Properties != nil && capture.Properties["internal"] == true {
					return nil
				}
			}
			return msg
		},
	})
	defer client.Close()

	// Enqueue mix of events
	events := []struct {
		event    string
		internal bool
	}{
		{"event1", false},
		{"event2", true}, // Should be dropped
		{"event3", false},
		{"event4", true}, // Should be dropped
		{"event5", false},
	}

	for _, e := range events {
		props := NewProperties()
		if e.internal {
			props["internal"] = true
		}
		client.Enqueue(Capture{
			Event:            e.event,
			DistinctId:       "user123",
			Properties:       props,
			SendFeatureFlags: SendFeatureFlags(false),
		})
	}

	// Wait for batch to be sent
	time.Sleep(50 * time.Millisecond)
	client.Close()

	// Should have sent 3 events (event1, event3, event5)
	if eventsSent != 3 {
		t.Errorf("Expected 3 events to be sent, got %d", eventsSent)
	}

	// Verify correct events were sent
	expectedEvents := []string{"event1", "event3", "event5"}
	if len(sentEvents) != len(expectedEvents) {
		t.Fatalf("Expected %d events, got %d", len(expectedEvents), len(sentEvents))
	}
	for i, expected := range expectedEvents {
		if sentEvents[i] != expected {
			t.Errorf("Expected event %s, got %s", expected, sentEvents[i])
		}
	}
}

// Test 10: BeforeSend with DefaultEventProperties
func TestBeforeSendWithDefaultEventProperties(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	var receivedCapture Capture
	client, _ := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 1,
		now:       mockTime,
		DefaultEventProperties: Properties{
			"default_prop": "default_value",
			"app_version":  "1.0.0",
		},
		BeforeSend: func(msg Message) Message {
			if capture, ok := msg.(Capture); ok {
				receivedCapture = capture
			}
			return msg
		},
	})
	defer client.Close()

	err := client.Enqueue(Capture{
		Event:            "test_event",
		DistinctId:       "user123",
		SendFeatureFlags: SendFeatureFlags(false),
	})

	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	<-body // Wait for message

	// Verify default properties are present when hook is called
	if receivedCapture.Properties == nil {
		t.Fatal("Expected Properties to be set")
	}
	if receivedCapture.Properties["default_prop"] != "default_value" {
		t.Error("Expected default_prop to be set before BeforeSend hook")
	}
	if receivedCapture.Properties["app_version"] != "1.0.0" {
		t.Error("Expected app_version to be set before BeforeSend hook")
	}
}
