package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/posthog/posthog-go"
)

const VERSION = "1.0.0"

// captureMode selects which capture protocol this adapter process speaks. It is
// baked at build time via the CAPTURE_MODE env var ("v1" => capture-v1, anything
// else => legacy v0), mirroring the v0/v1 Dockerfile split. One process speaks
// one mode and advertises it via /health capabilities.
var captureMode = os.Getenv("CAPTURE_MODE")

func isV1() bool { return captureMode == "v1" }

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
	if resp == nil {
		return resp, err
	}

	// Parse batch to get UUIDs. The request body shape is the same for v0 and
	// v1 ({"batch":[{"uuid":...}]}), so extraction is unchanged.
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

	// PostHog-Attempt is 1-based and only set on the v1 path. attempt-1 is the
	// retry index; attempt > 1 means this request is a retry.
	attempt := 1
	if a := req.Header.Get("PostHog-Attempt"); a != "" {
		if n, e := strconv.Atoi(a); e == nil && n > 0 {
			attempt = n
		}
	}

	// For v1, a 200 no longer means "all sent": read+restore the body and count
	// only terminal results (anything other than "retry") as sent.
	terminal := len(batch.Batch)
	if isV1() {
		var respBytes []byte
		if resp.Body != nil {
			respBytes, _ = io.ReadAll(resp.Body)
			resp.Body = io.NopCloser(bytes.NewBuffer(respBytes))
		}
		if resp.StatusCode == 200 {
			var parsed struct {
				Results map[string]struct {
					Result string `json:"result"`
				} `json:"results"`
			}
			terminal = 0
			if err := json.Unmarshal(respBytes, &parsed); err == nil {
				for _, r := range parsed.Results {
					if r.Result != "retry" {
						terminal++
					}
				}
			} else {
				log.Printf("[adapter] v1 response body unmarshal failed: %v (terminal=0, pending events may appear stuck)", err)
			}
		}
	}

	t.state.mu.Lock()
	t.state.requestsMade = append(t.state.requestsMade, RequestInfo{
		TimestampMs:  time.Now().UnixMilli(),
		StatusCode:   resp.StatusCode,
		RetryAttempt: attempt - 1,
		EventCount:   len(batch.Batch),
		UUIDList:     uuids,
	})
	if attempt > 1 {
		t.state.totalRetries++
	}
	if resp.StatusCode == 200 {
		t.state.totalEventsSent += terminal
		t.state.pendingEvents -= terminal
		if t.state.pendingEvents < 0 {
			t.state.pendingEvents = 0
		}
	}
	t.state.mu.Unlock()

	return resp, err
}

// AdapterState tracks SDK state for test assertions
type AdapterState struct {
	mu                  sync.Mutex
	client              posthog.Client
	config              *posthog.Config
	apiKey              string
	host                string
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
	APIKey              string `json:"api_key"`
	Host                string `json:"host"`
	FlushAt             *int   `json:"flush_at,omitempty"`
	FlushIntervalMs     *int   `json:"flush_interval_ms,omitempty"`
	MaxRetries          *int   `json:"max_retries,omitempty"`
	EnableCompression   *bool  `json:"enable_compression,omitempty"`
	DisableGeoIP        *bool  `json:"disable_geoip,omitempty"`
	HistoricalMigration *bool  `json:"historical_migration,omitempty"`
}

// CaptureRequest represents /capture endpoint request
type CaptureRequest struct {
	DistinctID string                 `json:"distinct_id"`
	Event      string                 `json:"event"`
	Properties map[string]interface{} `json:"properties,omitempty"`
	Timestamp  *string                `json:"timestamp,omitempty"`
	// Options carries capture-v1 event options (cookieless_mode,
	// disable_skew_correction, process_person_profile, product_tour_id, ...).
	// The adapter folds them back into magic event properties so the SDK lifts
	// them onto the wire options object.
	Options map[string]interface{} `json:"options,omitempty"`
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

// validateHarnessHost maps user-provided /init hosts to explicit SDK test
// harness mock targets. The adapter is only intended to call the harness mock
// server, so keep the outbound network target on a small allowlist.
func validateHarnessHost(raw string) (string, bool) {
	switch strings.TrimRight(raw, "/") {
	case "http://test-harness:8081":
		return "http://test-harness:8081", true
	case "http://localhost:8081":
		return "http://localhost:8081", true
	case "http://127.0.0.1:8081":
		return "http://127.0.0.1:8081", true
	default:
		return "", false
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	capabilities := []string{"capture_v0", "encoding_gzip"}
	if isV1() {
		capabilities = []string{"capture_v1", "encoding_gzip"}
	}
	response := HealthResponse{
		SDKName:        "posthog-go",
		SDKVersion:     posthog.Version,
		AdapterVersion: VERSION,
		Capabilities:   capabilities,
	}
	jsonResponse(w, response)
}

func initHandler(w http.ResponseWriter, r *http.Request) {
	var req InitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	validatedHost, ok := validateHarnessHost(req.Host)
	if !ok {
		http.Error(w, "host must be the SDK harness mock URL", http.StatusBadRequest)
		return
	}

	state.mu.Lock()
	oldClient := state.client
	state.client = nil

	// Reset state
	state.totalEventsCaptured = 0
	state.totalEventsSent = 0
	state.totalRetries = 0
	state.lastError = ""
	state.requestsMade = []RequestInfo{}
	state.pendingEvents = 0
	state.mu.Unlock()

	// Close the previous client outside state.mu. Close can wait for in-flight
	// sends whose tracked transport also records state under the same mutex.
	if oldClient != nil {
		oldClient.Close()
	}

	// Create new client with tracked transport
	config := posthog.Config{
		Endpoint:  validatedHost,
		Transport: &TrackedTransport{base: http.DefaultTransport, state: state},
		// Set test-friendly defaults
		BatchSize: 1,                     // Flush after each event by default
		Interval:  20 * time.Millisecond, // Short interval for tests
	}

	if isV1() {
		config.CaptureMode = posthog.CaptureModeAnalyticsV1
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
	if req.DisableGeoIP != nil {
		config.DisableGeoIP = req.DisableGeoIP
	}
	if req.HistoricalMigration != nil {
		config.HistoricalMigration = *req.HistoricalMigration
	}

	client, err := posthog.NewWithConfig(req.APIKey, config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	state.mu.Lock()
	state.client = client
	state.config = &config
	state.apiKey = req.APIKey
	state.host = validatedHost
	state.mu.Unlock()

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

	// Fold capture-v1 options back into magic event properties; the SDK lifts
	// them onto the wire options object. Unknown keys get a "$" prefix.
	if len(req.Options) > 0 {
		if capture.Properties == nil {
			capture.Properties = posthog.Properties{}
		}
		for k, v := range req.Options {
			switch k {
			case "cookieless_mode":
				capture.Properties["$cookieless_mode"] = v
			case "disable_skew_correction":
				capture.Properties["$ignore_sent_at"] = v
			case "process_person_profile":
				capture.Properties["$process_person_profile"] = v
			case "product_tour_id":
				capture.Properties["$product_tour_id"] = v
			default:
				capture.Properties["$"+k] = v
			}
		}
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

	// Wait only until the current queue drains, with a small cap. Most harness
	// tests use BatchSize=1, while batch-format tests rely on the short interval
	// above to flush partial batches. Avoid a fixed 500ms sleep per test: the
	// compliance suite has many flushes and long retry waits of its own.
	deadline := time.Now().Add(interval + (100 * time.Millisecond))
	for {
		state.mu.Lock()
		pendingEvents := state.pendingEvents
		eventsFlushed := state.totalEventsSent
		state.mu.Unlock()

		if pendingEvents == 0 || time.Now().After(deadline) {
			jsonResponse(w, map[string]interface{}{
				"success":        true,
				"events_flushed": eventsFlushed,
			})
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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

	// Sanitize user-controlled fields before they enter the SDK. The SDK
	// logs flag keys downstream (e.g. featureflags.go's local-eval Warnf),
	// which CodeQL flags as go/log-injection. Stripping CR/LF here breaks
	// the data flow at the adapter boundary.
	req.Key = sanitizeForLog(req.Key)
	req.DistinctID = sanitizeForLog(req.DistinctID)

	state.mu.Lock()
	client := state.client
	state.mu.Unlock()

	if client == nil {
		jsonError(w, http.StatusBadRequest, "SDK not initialized")
		return
	}

	state.mu.Lock()
	apiKey := state.apiKey
	host := state.host
	state.mu.Unlock()

	personProperties := map[string]interface{}{"distinct_id": req.DistinctID}
	for k, v := range req.PersonProperties {
		personProperties[k] = v
	}
	groups := map[string]interface{}{}
	for k, v := range req.Groups {
		groups[k] = v
	}
	groupProperties := map[string]interface{}{}
	for k, v := range req.GroupProperties {
		groupProperties[k] = v
	}
	geoipDisable := false
	if req.DisableGeoIP != nil {
		geoipDisable = *req.DisableGeoIP
	}

	payload := map[string]interface{}{
		"token":                 apiKey,
		"distinct_id":           req.DistinctID,
		"person_properties":     personProperties,
		"groups":                groups,
		"group_properties":      groupProperties,
		"geoip_disable":         geoipDisable,
		"flag_keys_to_evaluate": []string{req.Key},
	}
	body, _ := json.Marshal(payload)
	flagsURL := strings.TrimRight(host, "/") + "/flags/?v=2"
	resp, err := http.Post(flagsURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("Error evaluating feature flag: %s", sanitizeForLog(err.Error()))
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		jsonError(w, http.StatusInternalServerError, "flags request failed with status "+strconv.Itoa(resp.StatusCode))
		return
	}
	var decoded map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	value := interface{}(false)
	if flags, ok := decoded["featureFlags"].(map[string]interface{}); ok {
		if flagValue, ok := flags[req.Key]; ok {
			value = flagValue
		}
	}

	if err := client.Enqueue(posthog.Capture{
		DistinctId: req.DistinctID,
		Event:      "$feature_flag_called",
		Properties: posthog.Properties{
			"$feature_flag":          req.Key,
			"$feature_flag_response": value,
			"$feature/" + req.Key:    value,
		},
	}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state.mu.Lock()
	state.totalEventsCaptured++
	state.pendingEvents++
	state.mu.Unlock()

	// Avoid logging user-controlled fields (req.Key, req.DistinctID, value) to prevent log injection.
	log.Printf("Evaluated feature flag")

	jsonResponse(w, map[string]interface{}{
		"success": true,
		"value":   value,
	})
}

// sanitizeForLog strips CR/LF characters from a string before logging, so
// user-controlled input (e.g. error strings that quote upstream-supplied flag
// keys) cannot inject forged log entries via newline characters.
func sanitizeForLog(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	oldClient := state.client
	state.client = nil
	state.apiKey = ""
	state.host = ""
	state.totalEventsCaptured = 0
	state.totalEventsSent = 0
	state.totalRetries = 0
	state.lastError = ""
	state.requestsMade = []RequestInfo{}
	state.pendingEvents = 0
	state.mu.Unlock()

	// Close outside state.mu for the same reason as initHandler.
	if oldClient != nil {
		oldClient.Close()
	}

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
