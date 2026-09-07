package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/posthog/posthog-go"
)

const VERSION = "1.0.0"

// Each process selects one protocol and compression codec at runtime.
var captureMode = os.Getenv("CAPTURE_MODE")
var compression = os.Getenv("COMPRESSION")

func isV1() bool { return captureMode == "v1" }

func selectedCompression() (posthog.CompressionMode, error) {
	switch compression {
	case "", "gzip":
		return posthog.CompressionGzip, nil
	case "deflate":
		if isV1() {
			return posthog.CompressionDeflate, nil
		}
	case "br":
		if isV1() {
			return posthog.CompressionBrotli, nil
		}
	case "zstd":
		if isV1() {
			return posthog.CompressionZstd, nil
		}
	}
	return posthog.CompressionNone, fmt.Errorf("unsupported compression profile %q for capture mode %q", compression, captureMode)
}

// TrackedTransport observes SDK requests without implementing delivery policy.
type TrackedTransport struct {
	base  http.RoundTripper
	state *AdapterState
}

func decodeBody(body []byte, encoding string) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	switch encoding {
	case "":
		return body, nil
	case "gzip":
		reader, err = gzip.NewReader(bytes.NewReader(body))
	case "deflate":
		reader, err = zlib.NewReader(bytes.NewReader(body))
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	case "zstd":
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		return decoder.DecodeAll(body, nil)
	default:
		return nil, fmt.Errorf("unknown content encoding %q", encoding)
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (t *TrackedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
	}
	var batch struct {
		Batch []struct {
			UUID string `json:"uuid"`
		} `json:"batch"`
	}
	decoded, decodeErr := decodeBody(body, req.Header.Get("Content-Encoding"))
	if decodeErr == nil {
		decodeErr = json.Unmarshal(decoded, &batch)
	}
	uuids := []string{}
	for _, event := range batch.Batch {
		uuids = append(uuids, event.UUID)
	}

	t.state.mu.Lock()
	attempt := 0
	if strings.TrimRight(req.URL.Path, "/") == "/flags" {
		attempt = t.state.flagsAttempt
		t.state.flagsAttempt++
	} else if n, err := strconv.Atoi(req.Header.Get("PostHog-Attempt")); err == nil && n > 0 {
		attempt = n - 1
	} else if len(uuids) > 0 {
		key := strings.Join(uuids, ",")
		attempt = t.state.captureAttempts[key]
		t.state.captureAttempts[key]++
	}
	index := len(t.state.requestsMade)
	t.state.requestsMade = append(t.state.requestsMade, RequestInfo{
		TimestampMs: time.Now().UnixMilli(), RetryAttempt: attempt,
		EventCount: len(batch.Batch), UUIDList: uuids,
	})
	if attempt > 0 {
		t.state.totalRetries++
	}
	if decodeErr != nil {
		t.state.lastError = decodeErr.Error()
	}
	t.state.mu.Unlock()

	resp, err := t.base.RoundTrip(req)
	t.state.mu.Lock()
	if resp != nil {
		t.state.requestsMade[index].StatusCode = resp.StatusCode
	}
	if err != nil {
		t.state.lastError = err.Error()
	}
	t.state.mu.Unlock()
	return resp, err
}

// Public SDK hooks own identity and completion, including flag-called events,
// compression, terminal failures and partial V1 batches.
func (s *AdapterState) beforeSend(message posthog.Message) posthog.Message {
	if capture, ok := message.(posthog.Capture); ok {
		s.mu.Lock()
		s.lastUUID = capture.Uuid
		s.totalEventsCaptured++
		s.pendingEvents++
		s.mu.Unlock()
	}
	return message
}

func (s *AdapterState) Success(_ posthog.APIMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalEventsSent++
	s.pendingEvents--
}

func (s *AdapterState) Failure(_ posthog.APIMessage, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingEvents--
	s.lastError = err.Error()
}

// AdapterState tracks SDK state for test assertions
type AdapterState struct {
	mu                  sync.Mutex
	client              posthog.Client
	lastUUID            string
	flagsAttempt        int
	captureAttempts     map[string]int
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
	requestsMade:    []RequestInfo{},
	captureAttempts: map[string]int{},
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
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	switch u.Hostname() {
	case "test-harness", "localhost", "127.0.0.1", "::1":
	default:
		return "", false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return u.String(), true
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	capabilities := []string{"capture_v0", "encoding_gzip"}
	if isV1() {
		codec := compression
		if codec == "" {
			codec = "gzip"
		}
		capabilities = []string{"capture_v1", "encoding_" + codec}
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

	codec, err := selectedCompression()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	closeAndReset()

	// Create new client with tracked transport
	config := posthog.Config{
		Endpoint:   validatedHost,
		Transport:  &TrackedTransport{base: http.DefaultTransport, state: state},
		BeforeSend: state.beforeSend,
		Callback:   state,
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
			config.Compression = codec
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
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		capture.Timestamp = t
	}

	// BeforeSend runs synchronously inside Enqueue, after SDK UUID generation.
	state.mu.Lock()
	state.lastUUID = ""
	state.mu.Unlock()
	if err := state.client.Enqueue(capture); err != nil {
		// Queue rejection has no delivery callback, but enrichment may have run.
		if err == posthog.ErrQueueFull || err == posthog.ErrClosed {
			state.mu.Lock()
			if state.lastUUID != "" {
				state.pendingEvents--
			}
			state.lastError = err.Error()
			state.mu.Unlock()
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state.mu.Lock()
	uuid := state.lastUUID
	state.mu.Unlock()
	jsonResponse(w, map[string]interface{}{"success": true, "uuid": uuid})
}

func flushHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	initialized := state.client != nil
	state.mu.Unlock()
	if !initialized {
		jsonError(w, http.StatusBadRequest, "SDK not initialized")
		return
	}
	eventsFlushed, err := waitForPendingEvents(r.Context())
	if err != nil {
		jsonError(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	jsonResponse(w, map[string]interface{}{"success": true, "events_flushed": eventsFlushed})
}

// There is no non-closing public Flush. Wait for the configured SDK interval
// and terminal delivery callbacks, without closing/recreating the client.
func waitForPendingEvents(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state.mu.Lock()
	before := state.totalEventsSent
	state.mu.Unlock()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state.mu.Lock()
		pending, sent := state.pendingEvents, state.totalEventsSent
		state.mu.Unlock()
		if pending == 0 {
			return sent - before, nil
		}
		select {
		case <-ctx.Done():
			return sent - before, ctx.Err()
		case <-ticker.C:
		}
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

	groupProperties := make(map[string]posthog.Properties, len(req.GroupProperties))
	for key, properties := range req.GroupProperties {
		groupProperties[key] = properties
	}
	state.mu.Lock()
	state.flagsAttempt = 0
	state.mu.Unlock()
	// No personal API key/local evaluator is configured. Each EvaluateFlags
	// action makes a remote request even when force_remote is false or omitted.
	snapshot, err := client.EvaluateFlags(posthog.EvaluateFlagsPayload{
		DistinctId:       req.DistinctID,
		PersonProperties: req.PersonProperties,
		Groups:           req.Groups,
		GroupProperties:  groupProperties,
		DisableGeoIP:     req.DisableGeoIP,
		FlagKeys:         []string{req.Key},
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	value := snapshot.GetFlag(req.Key)
	jsonResponse(w, map[string]interface{}{"success": true, "value": value})
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

func closeAndReset() {
	state.mu.Lock()
	oldClient := state.client
	state.client = nil
	state.mu.Unlock()
	// Close before clearing observations: SDK shutdown may deliver a final batch.
	if oldClient != nil {
		oldClient.Close()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastUUID = ""
	state.totalEventsCaptured = 0
	state.totalEventsSent = 0
	state.totalRetries = 0
	state.lastError = ""
	state.requestsMade = []RequestInfo{}
	state.pendingEvents = 0
	state.flagsAttempt = 0
	state.captureAttempts = map[string]int{}
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	closeAndReset()
	jsonResponse(w, map[string]bool{"success": true})
}

// The adapter has one SDK client, so lifecycle/actions are serialized. State
// remains readable while flush waits; parallel test isolation is not advertised.
var actionMu sync.Mutex

func serial(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actionMu.Lock()
		defer actionMu.Unlock()
		handler(w, r)
	}
}

func main() {
	if _, err := selectedCompression(); err != nil {
		log.Fatal(err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/init", serial(initHandler))
	http.HandleFunc("/capture", serial(captureHandler))
	http.HandleFunc("/flush", serial(flushHandler))
	http.HandleFunc("/state", stateHandler)
	http.HandleFunc("/reset", serial(resetHandler))
	http.HandleFunc("/get_feature_flag", serial(featureFlagHandler))

	log.Printf("Starting PostHog Go SDK adapter on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
