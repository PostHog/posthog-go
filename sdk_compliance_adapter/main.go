package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go/v2"
)

const VERSION = "1.0.0"

// compressionCodecs maps the COMPRESSION env var to the codec that /init's
// enable_compression turns on. The harness has one enable_compression flag but
// a test per codec, so CI runs one adapter per codec and each one advertises
// only its own encoding_<codec> capability.
var compressionCodecs = map[string]posthog.CompressionMode{
	"gzip":    posthog.CompressionGzip,
	"deflate": posthog.CompressionDeflate,
	"br":      posthog.CompressionBrotli,
	"zstd":    posthog.CompressionZstd,
}

// compressionName is the COMPRESSION codec this adapter uses, set in main.
var compressionName = "gzip"

func compressionFromEnv() (string, bool) {
	name := os.Getenv("COMPRESSION")
	if name == "" {
		return "gzip", true
	}
	_, ok := compressionCodecs[name]
	return name, ok
}

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

	// Parse batch to get UUIDs from the request envelope
	// ({"batch":[{"uuid":...}]}).
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

	// PostHog-Attempt is 1-based. attempt-1 is the retry index; attempt > 1
	// means this request is a retry.
	attempt := 1
	if a := req.Header.Get("PostHog-Attempt"); a != "" {
		if n, e := strconv.Atoi(a); e == nil && n > 0 {
			attempt = n
		}
	}

	// A 200 does not mean "all sent": read+restore the body and count only
	// terminal results (anything other than "retry") as sent.
	terminal := len(batch.Batch)
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
			log.Printf("[adapter] capture response body unmarshal failed: %v (terminal=0, pending events may appear stuck)", err)
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
	reportedDrops       []ReportedDrop
	logLines            []string
}

// ReportedDrop is one event the SDK reported as dropped through its Callback.
type ReportedDrop struct {
	UUID    string `json:"uuid"`
	Event   string `json:"event"`
	Details string `json:"details"`
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
	requestsMade:  []RequestInfo{},
	reportedDrops: []ReportedDrop{},
	logLines:      []string{},
}

// clearDiagnostics empties what /state reports from the SDK's own error and log
// surfaces. Call it after the previous client is closed, because Close can still
// log and report failures.
func (s *AdapterState) clearDiagnostics() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportedDrops = []ReportedDrop{}
	s.logLines = []string{}
}

// stateCallback records every event the SDK fails to send, so /state can report
// drops (reported_drops). Success needs no tracking: TrackedTransport counts it.
type stateCallback struct {
	state *AdapterState
}

func (c stateCallback) Success(posthog.APIMessage) {}

func (c stateCallback) Failure(msg posthog.APIMessage, err error) {
	drop := dropFromFailure(msg, err)
	var localErr *posthog.CaptureLocalError
	c.state.mu.Lock()
	c.state.reportedDrops = append(c.state.reportedDrops, drop)
	// An event the SDK refused itself never reaches TrackedTransport, so it
	// would stay pending forever and make /flush wait out its full timeout.
	if errors.As(err, &localErr) && c.state.pendingEvents > 0 {
		c.state.pendingEvents--
	}
	c.state.mu.Unlock()
}

// dropFromFailure names the event behind a Callback.Failure. Details is the
// capture endpoint's per-event tag (such as invalid_options or
// duplicate_event_uuid) when it gave one, else the SDK's own error text.
func dropFromFailure(msg posthog.APIMessage, err error) ReportedDrop {
	drop := ReportedDrop{Details: err.Error()}
	switch m := msg.(type) {
	case posthog.CaptureInApi:
		drop.UUID, drop.Event = m.Uuid, m.Event
	case *posthog.CaptureInApi:
		drop.UUID, drop.Event = m.Uuid, m.Event
	}
	var eventErr *posthog.CaptureEventError
	if errors.As(err, &eventErr) {
		drop.UUID = eventErr.EventUUID
		if eventErr.Details != "" {
			drop.Details = eventErr.Details
		}
	}
	return drop
}

// stateLogger records every line the SDK logs for /state (log_capture) and still
// prints it through the standard logger.
type stateLogger struct {
	state  *AdapterState
	passTo posthog.Logger
}

func newStateLogger(state *AdapterState) posthog.Logger {
	return stateLogger{state: state, passTo: posthog.StdLogger(log.New(os.Stderr, "posthog ", log.LstdFlags), false)}
}

func (l stateLogger) record(level, format string, args []interface{}) {
	line := level + " " + fmt.Sprintf(format, args...)
	l.state.mu.Lock()
	l.state.logLines = append(l.state.logLines, line)
	l.state.mu.Unlock()
}

func (l stateLogger) Debugf(format string, args ...interface{}) {
	l.record("DEBUG", format, args)
	l.passTo.Debugf(format, args...)
}

func (l stateLogger) Logf(format string, args ...interface{}) {
	l.record("INFO", format, args)
	l.passTo.Logf(format, args...)
}

func (l stateLogger) Warnf(format string, args ...interface{}) {
	l.record("WARN", format, args)
	l.passTo.Warnf(format, args...)
}

func (l stateLogger) Errorf(format string, args ...interface{}) {
	l.record("ERROR", format, args)
	l.passTo.Errorf(format, args...)
}

// paddingKey is the property /capture and /capture_ai add for
// pad_properties_to_bytes.
const paddingKey = "$test_padding"

// compactJSONSize is the byte length of v as compact UTF-8 JSON, the way the
// harness measures properties. HTML escaping is off because it would lengthen
// the output.
func compactJSONSize(v interface{}) (int, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return 0, err
	}
	return buf.Len() - 1, nil // Encode appends a newline
}

// padProperties returns a copy of properties with one string property that makes
// the compact JSON of the whole map exactly size bytes. The padding starts with
// marker and continues with "x".
func padProperties(properties map[string]interface{}, size int, marker string) (map[string]interface{}, error) {
	padded := make(map[string]interface{}, len(properties)+1)
	for k, v := range properties {
		padded[k] = v
	}
	padded[paddingKey] = ""
	base, err := compactJSONSize(padded)
	if err != nil {
		return nil, err
	}
	fill := size - base
	if fill < len(marker) {
		return nil, fmt.Errorf("pad_properties_to_bytes %d is too small for the properties and marker", size)
	}
	padded[paddingKey] = marker + strings.Repeat("x", fill-len(marker))
	return padded, nil
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
	// SuperProperties become the SDK's DefaultEventProperties.
	SuperProperties map[string]interface{} `json:"super_properties,omitempty"`
	// BeforeSend installs a hook that sets SetProperties on every event.
	BeforeSend *BeforeSendRequest `json:"before_send,omitempty"`
}

// BeforeSendRequest describes the BeforeSend hook /init installs.
type BeforeSendRequest struct {
	SetProperties map[string]interface{} `json:"set_properties"`
}

// setPropertiesHook returns a BeforeSend that sets each of set on the properties
// of a Capture and passes every other message through unchanged.
func setPropertiesHook(set map[string]interface{}) posthog.BeforeSendFunc {
	return func(msg posthog.Message) posthog.Message {
		capture, ok := msg.(posthog.Capture)
		if !ok {
			return msg
		}
		if capture.Properties == nil {
			capture.Properties = posthog.Properties{}
		}
		for k, v := range set {
			capture.Properties[k] = v
		}
		return capture
	}
}

// CaptureRequest represents /capture endpoint request
type CaptureRequest struct {
	DistinctID string                 `json:"distinct_id"`
	Event      string                 `json:"event"`
	Properties map[string]interface{} `json:"properties,omitempty"`
	Timestamp  *string                `json:"timestamp,omitempty"`
	// Options carries capture event options (cookieless_mode,
	// disable_skew_correction, process_person_profile, product_tour_id, ...),
	// passed to the SDK unchanged.
	Options posthog.Options `json:"options,omitempty"`
	// PadPropertiesToBytes asks for a $test_padding property that makes the
	// properties' compact JSON exactly this long, so the harness can send
	// multi-megabyte events without a multi-megabyte request. Marker starts the
	// padding string.
	PadPropertiesToBytes *int   `json:"pad_properties_to_bytes,omitempty"`
	Marker               string `json:"marker,omitempty"`
	// UUID is the caller's event UUID, which must stay on the wire. Without
	// one the adapter generates it, so /capture can return the UUID it sent.
	UUID string `json:"uuid,omitempty"`
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
	PendingEvents       int            `json:"pending_events"`
	TotalEventsCaptured int            `json:"total_events_captured"`
	TotalEventsSent     int            `json:"total_events_sent"`
	TotalRetries        int            `json:"total_retries"`
	LastError           string         `json:"last_error,omitempty"`
	RequestsMade        []RequestInfo  `json:"requests_made"`
	ReportedDrops       []ReportedDrop `json:"reported_drops"`
	LogLines            []string       `json:"log_lines"`
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
	// Harness contract keys: capture_v1 and capture_ai_v1 select the v1 suites,
	// event_options selects the option pass-through tests, event_uuid_override,
	// bounded_retries, disable_geoip and historical_migration select the tests
	// for those adapter params, and encoding_<codec> selects the compression
	// test for this adapter's codec. They are external strings, not internal
	// version markers.
	capabilities := []string{
		"capture_v1", "capture_ai_v1", "event_options",
		"event_uuid_override", "bounded_retries", "disable_geoip", "historical_migration",
		"reported_drops", "log_capture", "generated_payloads", "property_layers", "fail_open",
		"encoding_" + compressionName,
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
	state.clearDiagnostics()

	// Create new client with tracked transport
	config := posthog.Config{
		Endpoint:  validatedHost,
		Transport: &TrackedTransport{base: http.DefaultTransport, state: state},
		Callback:  stateCallback{state: state},
		Logger:    newStateLogger(state),
		// Set test-friendly defaults
		BatchSize: 1,                     // Flush after each event by default
		Interval:  20 * time.Millisecond, // Short interval for tests
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
	if req.EnableCompression != nil && *req.EnableCompression {
		config.Compression = compressionCodecs[compressionName]
		config.CaptureAICompression = compressionCodecs[compressionName]
	}
	if req.DisableGeoIP != nil {
		config.DisableGeoIP = req.DisableGeoIP
	}
	if req.HistoricalMigration != nil {
		config.HistoricalMigration = *req.HistoricalMigration
	}

	if len(req.SuperProperties) > 0 {
		config.DefaultEventProperties = posthog.Properties(req.SuperProperties)
	}
	if req.BeforeSend != nil {
		config.BeforeSend = setPropertiesHook(req.BeforeSend.SetProperties)
	}

	// An empty or personal (phx_) key goes to the SDK as given: it disables
	// itself instead of failing (fail_open).
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
	handleCapture(w, r, posthog.Client.Enqueue)
}

// captureAIHandler sends through EnqueueAI, the SDK's AI capture lane.
func captureAIHandler(w http.ResponseWriter, r *http.Request) {
	handleCapture(w, r, posthog.Client.EnqueueAI)
}

func handleCapture(w http.ResponseWriter, r *http.Request, enqueue func(posthog.Client, posthog.Message) error) {
	var req CaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.mu.Lock()
	client := state.client
	state.mu.Unlock()
	if client == nil {
		http.Error(w, "SDK not initialized", http.StatusBadRequest)
		return
	}

	// Count the event before enqueueing it: the transport can deliver it and
	// decrement pendingEvents before Enqueue returns.
	state.mu.Lock()
	state.totalEventsCaptured++
	state.pendingEvents++
	state.mu.Unlock()

	capture, err := buildCaptureWithPadding(req)
	if err != nil {
		state.mu.Lock()
		state.totalEventsCaptured--
		state.pendingEvents--
		state.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := enqueue(client, capture); err != nil {
		state.mu.Lock()
		state.totalEventsCaptured--
		state.pendingEvents--
		state.mu.Unlock()
		// A disabled client (empty or personal API key) drops the event by
		// design, which is a success from the caller's side (fail_open).
		if errors.Is(err, posthog.ErrSDKDisabled) {
			jsonResponse(w, map[string]interface{}{
				"success": true,
				"uuid":    capture.Uuid,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"success": true,
		"uuid":    capture.Uuid,
	})
}

// newEventUUID returns a v7 UUID, or a v4 one if the clock-based path fails,
// like the SDK does.
func newEventUUID() string {
	if v7, err := uuid.NewV7(); err == nil {
		return v7.String()
	}
	return uuid.New().String()
}

// buildCapture maps a harness capture request onto the SDK message. Options
// pass through unchanged, and the UUID is the caller's valid one or a new one,
// so the response can name the event that was sent.
func buildCapture(req CaptureRequest) posthog.Capture {
	capture := posthog.Capture{
		Uuid:       req.UUID,
		DistinctId: req.DistinctID,
		Event:      req.Event,
		Properties: req.Properties,
		Options:    req.Options,
	}
	// The SDK replaces an empty or invalid UUID, so replace it here first and
	// return the UUID that is actually sent.
	if capture.Uuid == "" || uuid.Validate(capture.Uuid) != nil {
		capture.Uuid = newEventUUID()
	}
	if req.Timestamp != nil {
		if t, err := time.Parse(time.RFC3339, *req.Timestamp); err == nil {
			capture.Timestamp = t
		}
	}
	return capture
}

// buildCaptureWithPadding is buildCapture plus the generated padding property.
func buildCaptureWithPadding(req CaptureRequest) (posthog.Capture, error) {
	if req.PadPropertiesToBytes != nil {
		padded, err := padProperties(req.Properties, *req.PadPropertiesToBytes, req.Marker)
		if err != nil {
			return posthog.Capture{}, err
		}
		req.Properties = padded
	}
	return buildCapture(req), nil
}

func flushHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	if state.client == nil {
		state.mu.Unlock()
		http.Error(w, "SDK not initialized", http.StatusBadRequest)
		return
	}
	state.mu.Unlock()

	eventsFlushed := waitForPendingEvents()
	jsonResponse(w, map[string]interface{}{
		"success":        true,
		"events_flushed": eventsFlushed,
	})
}

func waitForPendingEvents() int {
	state.mu.Lock()
	interval := state.config.Interval
	if interval == 0 {
		interval = 5 * time.Second // Default
	}
	state.mu.Unlock()

	// Wait only until the current queue drains, with a small cap. Most harness
	// tests use BatchSize=1, while batch-format tests rely on the short interval
	// above to flush partial batches. Avoid fixed sleeps per test: the compliance
	// suite has many flushes and long retry waits of its own.
	deadline := time.Now().Add(interval + (100 * time.Millisecond))
	for {
		state.mu.Lock()
		pendingEvents := state.pendingEvents
		eventsFlushed := state.totalEventsSent
		state.mu.Unlock()

		if pendingEvents == 0 || time.Now().After(deadline) {
			return eventsFlushed
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
		ReportedDrops:       append([]ReportedDrop{}, state.reportedDrops...),
		LogLines:            append([]string{}, state.logLines...),
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
		"api_key":               apiKey,
		"distinct_id":           req.DistinctID,
		"person_properties":     personProperties,
		"groups":                groups,
		"group_properties":      groupProperties,
		"geoip_disable":         geoipDisable,
		"flag_keys_to_evaluate": []string{req.Key},
	}
	body, _ := json.Marshal(payload)
	flagsURL := strings.TrimRight(host, "/") + "/flags/?v=2"
	resp, err := postFlagsWithRetry(flagsURL, body)
	if err != nil {
		log.Printf("Error evaluating feature flag: %s", sanitizeForLog(err.Error()))
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer resp.Body.Close()
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
	properties := posthog.Properties{
		"$feature_flag":          req.Key,
		"$feature_flag_response": value,
		"$feature/" + req.Key:    value,
	}
	// Only send $feature_flag_has_experiment when the server explicitly
	// reported has_experiment on the flag's metadata; omit it when unknown.
	if flags, ok := decoded["flags"].(map[string]interface{}); ok {
		if flagDetail, ok := flags[req.Key].(map[string]interface{}); ok {
			if metadata, ok := flagDetail["metadata"].(map[string]interface{}); ok {
				if v, ok := metadata["has_experiment"].(bool); ok {
					properties["$feature_flag_has_experiment"] = v
				}
			}
		}
	}

	if err := client.Enqueue(posthog.Capture{
		DistinctId: req.DistinctID,
		Event:      "$feature_flag_called",
		Properties: properties,
	}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state.mu.Lock()
	state.totalEventsCaptured++
	state.pendingEvents++
	state.mu.Unlock()
	waitForPendingEvents()

	// Avoid logging user-controlled fields (req.Key, req.DistinctID, value) to prevent log injection.
	log.Printf("Evaluated feature flag")

	jsonResponse(w, map[string]interface{}{
		"success": true,
		"value":   value,
	})
}

func postFlagsWithRetry(flagsURL string, body []byte) (*http.Response, error) {
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := http.Post(flagsURL, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return resp, nil
		}

		lastStatus = resp.StatusCode
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusGatewayTimeout {
			break
		}
	}

	return nil, &flagsStatusError{statusCode: lastStatus}
}

type flagsStatusError struct {
	statusCode int
}

func (e *flagsStatusError) Error() string {
	return "flags request failed with status " + strconv.Itoa(e.statusCode)
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
	state.clearDiagnostics()

	jsonResponse(w, map[string]bool{"success": true})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	name, ok := compressionFromEnv()
	if !ok {
		log.Fatalf("unsupported COMPRESSION %q: want gzip, deflate, br or zstd", name)
	}
	compressionName = name

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/init", initHandler)
	http.HandleFunc("/capture", captureHandler)
	http.HandleFunc("/capture_ai", captureAIHandler)
	http.HandleFunc("/flush", flushHandler)
	http.HandleFunc("/state", stateHandler)
	http.HandleFunc("/reset", resetHandler)
	http.HandleFunc("/get_feature_flag", featureFlagHandler)

	log.Printf("Starting PostHog Go SDK adapter on port %s (compression=%s)", port, compressionName)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
