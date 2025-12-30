package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/posthog/posthog-go"
)

const VERSION = "1.0.0"

// TrackedTransport wraps http.RoundTripper to track requests
type TrackedTransport struct {
	base  http.RoundTripper
	state *AdapterState
}

func (t *TrackedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Read and restore request body to extract UUIDs
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// Make the request
	resp, err := t.base.RoundTrip(req)

	// Track the request
	if resp != nil {
		// Parse batch to get UUIDs
		var batch struct {
			Batch []struct {
				UUID string `json:"uuid"`
			} `json:"batch"`
		}
		uuids := []string{}
		if len(bodyBytes) > 0 {
			json.Unmarshal(bodyBytes, &batch)
			for _, event := range batch.Batch {
				if event.UUID != "" {
					uuids = append(uuids, event.UUID)
				}
			}
		}

		t.state.mu.Lock()
		t.state.requestsMade = append(t.state.requestsMade, RequestInfo{
			TimestampMs:  time.Now().UnixMilli(),
			StatusCode:   resp.StatusCode,
			RetryAttempt: 0, // TODO: Track retries
			EventCount:   len(batch.Batch),
			UUIDList:     uuids,
		})

		if resp.StatusCode == 200 {
			t.state.totalEventsSent += len(batch.Batch)
			t.state.pendingEvents -= len(batch.Batch)
			if t.state.pendingEvents < 0 {
				t.state.pendingEvents = 0
			}
		}
		t.state.mu.Unlock()
	}

	return resp, err
}

// AdapterState tracks SDK state for test assertions
type AdapterState struct {
	mu                    sync.Mutex
	client                posthog.Client
	config                *posthog.Config
	totalEventsCaptured   int
	totalEventsSent       int
	totalRetries          int
	lastError             string
	requestsMade          []RequestInfo
	pendingEvents         int
}

// RequestInfo tracks HTTP request details
type RequestInfo struct {
	TimestampMs  int64    `json:"timestamp_ms"`
	StatusCode   int      `json:"status_code"`
	RetryAttempt int      `json:"retry_attempt"`
	EventCount   int      `json:"event_count"`
	UUIDList     []string `json:"uuid_list"`
}

var state = &AdapterState{
	requestsMade: []RequestInfo{},
}

// HealthResponse represents /health endpoint response
type HealthResponse struct {
	SDKName        string `json:"sdk_name"`
	SDKVersion     string `json:"sdk_version"`
	AdapterVersion string `json:"adapter_version"`
}

// InitRequest represents /init endpoint request
type InitRequest struct {
	APIKey            string `json:"api_key"`
	Host              string `json:"host"`
	FlushAt           *int   `json:"flush_at,omitempty"`
	FlushIntervalMs   *int   `json:"flush_interval_ms,omitempty"`
	MaxRetries        *int   `json:"max_retries,omitempty"`
	EnableCompression *bool  `json:"enable_compression,omitempty"`
}

// CaptureRequest represents /capture endpoint request
type CaptureRequest struct {
	DistinctID string                 `json:"distinct_id"`
	Event      string                 `json:"event"`
	Properties map[string]interface{} `json:"properties,omitempty"`
	Timestamp  *string                `json:"timestamp,omitempty"`
}

// StateResponse represents /state endpoint response
type StateResponse struct {
	PendingEvents         int           `json:"pending_events"`
	TotalEventsCaptured   int           `json:"total_events_captured"`
	TotalEventsSent       int           `json:"total_events_sent"`
	TotalRetries          int           `json:"total_retries"`
	LastError             string        `json:"last_error,omitempty"`
	RequestsMade          []RequestInfo `json:"requests_made"`
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		SDKName:        "posthog-go",
		SDKVersion:     posthog.Version,
		AdapterVersion: VERSION,
	}
	jsonResponse(w, response)
}

func initHandler(w http.ResponseWriter, r *http.Request) {
	var req InitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// Close existing client if any
	if state.client != nil {
		state.client.Close()
	}

	// Reset state
	state.totalEventsCaptured = 0
	state.totalEventsSent = 0
	state.totalRetries = 0
	state.lastError = ""
	state.requestsMade = []RequestInfo{}
	state.pendingEvents = 0

	// Create new client with tracked transport
	config := posthog.Config{
		Endpoint:  req.Host,
		Transport: &TrackedTransport{base: http.DefaultTransport, state: state},
		// Set test-friendly defaults
		BatchSize: 1,                   // Flush after each event by default
		Interval:  100 * time.Millisecond, // Short interval for tests
	}

	// Override with request params if provided
	if req.FlushAt != nil {
		config.BatchSize = *req.FlushAt
	}
	if req.FlushIntervalMs != nil {
		config.Interval = time.Duration(*req.FlushIntervalMs) * time.Millisecond
	}

	client, err := posthog.NewWithConfig(req.APIKey, config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	state.client = client
	state.config = &config

	jsonResponse(w, map[string]bool{"success": true})
}

func captureHandler(w http.ResponseWriter, r *http.Request) {
	var req CaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.mu.Lock()
	if state.client == nil {
		state.mu.Unlock()
		http.Error(w, "SDK not initialized", http.StatusBadRequest)
		return
	}
	state.mu.Unlock()

	// Create capture event
	capture := posthog.Capture{
		DistinctId: req.DistinctID,
		Event:      req.Event,
		Properties: req.Properties,
	}

	if req.Timestamp != nil {
		// Parse timestamp if provided
		t, err := time.Parse(time.RFC3339, *req.Timestamp)
		if err == nil {
			capture.Timestamp = t
		}
	}

	// Enqueue event
	if err := state.client.Enqueue(capture); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	state.mu.Lock()
	state.totalEventsCaptured++
	state.pendingEvents++
	state.mu.Unlock()

	// TODO: Get actual UUID from SDK
	jsonResponse(w, map[string]interface{}{
		"success": true,
		"uuid":    "generated-uuid",
	})
}

func flushHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	if state.client == nil {
		state.mu.Unlock()
		http.Error(w, "SDK not initialized", http.StatusBadRequest)
		return
	}

	// Get configured interval
	interval := state.config.Interval
	if interval == 0 {
		interval = 5 * time.Second // Default
	}
	state.mu.Unlock()

	// Wait for events to be sent
	// Add extra buffer time to account for network delays
	waitTime := interval + (500 * time.Millisecond)
	time.Sleep(waitTime)

	state.mu.Lock()
	eventsFlushed := state.totalEventsSent
	state.mu.Unlock()

	jsonResponse(w, map[string]interface{}{
		"success":        true,
		"events_flushed": eventsFlushed,
	})
}

func stateHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	defer state.mu.Unlock()

	response := StateResponse{
		PendingEvents:       state.pendingEvents,
		TotalEventsCaptured: state.totalEventsCaptured,
		TotalEventsSent:     state.totalEventsSent,
		TotalRetries:        state.totalRetries,
		LastError:           state.lastError,
		RequestsMade:        state.requestsMade,
	}

	jsonResponse(w, response)
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.client != nil {
		state.client.Close()
		state.client = nil
	}

	state.totalEventsCaptured = 0
	state.totalEventsSent = 0
	state.totalRetries = 0
	state.lastError = ""
	state.requestsMade = []RequestInfo{}
	state.pendingEvents = 0

	jsonResponse(w, map[string]bool{"success": true})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/init", initHandler)
	http.HandleFunc("/capture", captureHandler)
	http.HandleFunc("/flush", flushHandler)
	http.HandleFunc("/state", stateHandler)
	http.HandleFunc("/reset", resetHandler)

	log.Printf("Starting PostHog Go SDK adapter on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
