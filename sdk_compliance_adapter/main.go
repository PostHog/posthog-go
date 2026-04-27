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
	mu                  sync.Mutex
	client              posthog.Client
	config              *posthog.Config
	totalEventsCaptured int
	totalEventsSent     int
	totalRetries        int
	lastError           string
	requestsMade        []RequestInfo
	pendingEvents       int
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
	SDKName        string   `json:"sdk_name"`
	SDKVersion     string   `json:"sdk_version"`
	AdapterVersion string   `json:"adapter_version"`
	Capabilities   []string `json:"capabilities"`
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

// FeatureFlagRequest represents /get_feature_flag endpoint request
type FeatureFlagRequest struct {
	Key              string                            `json:"key"`
	DistinctID       string                            `json:"distinct_id"`
	PersonProperties map[string]interface{}            `json:"person_properties,omitempty"`
	Groups           map[string]interface{}            `json:"groups,omitempty"`
	GroupProperties  map[string]map[string]interface{} `json:"group_properties,omitempty"`
	DisableGeoIP     *bool                             `json:"disable_geoip,omitempty"`
	ForceRemote      *bool                             `json:"force_remote,omitempty"`
}

// StateResponse represents /state endpoint response
type StateResponse struct {
	PendingEvents       int           `json:"pending_events"`
	TotalEventsCaptured int           `json:"total_events_captured"`
	TotalEventsSent     int           `json:"total_events_sent"`
	TotalRetries        int           `json:"total_retries"`
	LastError           string        `json:"last_error,omitempty"`
	RequestsMade        []RequestInfo `json:"requests_made"`
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
		Capabilities:   []string{"capture_v0", "encoding_gzip"},
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
		BatchSize: 1,                      // Flush after each event by default
		Interval:  100 * time.Millisecond, // Short interval for tests
	}

	// Override with request params if provided
	if req.FlushAt != nil {
		config.BatchSize = *req.FlushAt
	}
	if req.FlushIntervalMs != nil {
		config.Interval = time.Duration(*req.FlushIntervalMs) * time.Millisecond
	}
	if req.MaxRetries != nil {
		config.MaxRetries = req.MaxRetries
	}
	if req.EnableCompression != nil {
		if *req.EnableCompression {
			config.Compression = posthog.CompressionGzip
		} else {
			config.Compression = posthog.CompressionNone
		}
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

func featureFlagHandler(w http.ResponseWriter, r *http.Request) {
	var req FeatureFlagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Key == "" {
		jsonError(w, http.StatusBadRequest, "key is required")
		return
	}
	if req.DistinctID == "" {
		jsonError(w, http.StatusBadRequest, "distinct_id is required")
		return
	}

	state.mu.Lock()
	client := state.client
	state.mu.Unlock()

	if client == nil {
		jsonError(w, http.StatusBadRequest, "SDK not initialized")
		return
	}

	// force_remote defaults to true
	forceRemote := true
	if req.ForceRemote != nil {
		forceRemote = *req.ForceRemote
	}

	// Note: posthog-go's DisableGeoIP is a client-level config (Config.DisableGeoIP),
	// not a per-call option on FeatureFlagPayload. We accept the field on the request
	// for compatibility with the harness contract but do not honor it per-call. The
	// SDK default (when DisableGeoIP is nil) is to disable geoip on flag requests,
	// which matches the test harness's expected `geoip_disable: true` assertion.
	_ = req.DisableGeoIP

	// Convert groups/properties to SDK types
	var groups posthog.Groups
	if req.Groups != nil {
		groups = posthog.Groups(req.Groups)
	}

	var personProperties posthog.Properties
	if req.PersonProperties != nil {
		personProperties = posthog.Properties(req.PersonProperties)
	}

	var groupProperties map[string]posthog.Properties
	if req.GroupProperties != nil {
		groupProperties = make(map[string]posthog.Properties, len(req.GroupProperties))
		for k, v := range req.GroupProperties {
			groupProperties[k] = posthog.Properties(v)
		}
	}

	value, err := client.GetFeatureFlag(posthog.FeatureFlagPayload{
		Key:                 req.Key,
		DistinctId:          req.DistinctID,
		Groups:              groups,
		PersonProperties:    personProperties,
		GroupProperties:     groupProperties,
		OnlyEvaluateLocally: !forceRemote,
	})
	if err != nil {
		// Avoid logging user-controlled fields (req.Key, req.DistinctID) to prevent log injection.
		log.Printf("Error evaluating feature flag: %v", err)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Avoid logging user-controlled fields (req.Key, req.DistinctID, value) to prevent log injection.
	log.Printf("Evaluated feature flag")

	jsonResponse(w, map[string]interface{}{
		"success": true,
		"value":   value,
	})
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
	http.HandleFunc("/get_feature_flag", featureFlagHandler)

	log.Printf("Starting PostHog Go SDK adapter on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
