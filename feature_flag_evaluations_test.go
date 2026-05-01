package posthog

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// flagsServer wraps an httptest server that responds with the v4 flags fixture.
// It records every call to the /flags endpoint and exposes the request body so
// tests can assert on flag_keys_to_evaluate and other forwarded fields.
type flagsServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	calls    []FlagsRequestData
	rawCalls []string
}

func newFlagsServer(t *testing.T, fixtureName string) *flagsServer {
	t.Helper()
	fs := &flagsServer{}
	fs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			body := new(bytes.Buffer)
			body.ReadFrom(r.Body)
			fs.mu.Lock()
			fs.rawCalls = append(fs.rawCalls, body.String())
			var parsed FlagsRequestData
			_ = json.Unmarshal(body.Bytes(), &parsed)
			fs.calls = append(fs.calls, parsed)
			fs.mu.Unlock()
			w.Write([]byte(fixture(fixtureName)))
		}
	}))
	return fs
}

func (fs *flagsServer) close() { fs.server.Close() }

func (fs *flagsServer) callCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.calls)
}

func (fs *flagsServer) lastCall() (FlagsRequestData, string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.calls) == 0 {
		return FlagsRequestData{}, ""
	}
	return fs.calls[len(fs.calls)-1], fs.rawCalls[len(fs.rawCalls)-1]
}

// captureLogger collects warnings logged via the SDK Logger interface so tests
// can assert on filter-helper warnings.
type captureLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (l *captureLogger) Debugf(string, ...interface{}) {}
func (l *captureLogger) Logf(string, ...interface{})   {}
func (l *captureLogger) Errorf(string, ...interface{}) {}
func (l *captureLogger) Warnf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, formatLog(format, args...))
}

func (l *captureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.warnings))
	copy(out, l.warnings)
	return out
}

// formatLog mimics fmt.Sprintf without pulling fmt into the test body.
func formatLog(format string, args ...interface{}) string {
	if len(args) == 0 {
		return format
	}
	// Use json marshal as a cheap stringifier for our test purposes.
	parts := []string{format}
	for _, a := range args {
		b, _ := json.Marshal(a)
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "|")
}

func newEvalClient(t *testing.T, server *httptest.Server, opts ...func(*Config)) (Client, *eventCapture, *captureLogger) {
	t.Helper()
	capture := &eventCapture{}
	logger := &captureLogger{}
	cfg := Config{
		Endpoint:  server.URL,
		BatchSize: 1, // deliver each enqueued event immediately for inspection
		Callback:  capture,
		Logger:    logger,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	cli, err := NewWithConfig("test-api-key", cfg)
	if err != nil {
		t.Fatalf("NewWithConfig failed: %v", err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli, capture, logger
}

func waitForEventCount(capture *eventCapture, want int, timeout time.Duration) []CaptureInApi {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		if len(capture.events) >= want {
			out := make([]CaptureInApi, len(capture.events))
			copy(out, capture.events)
			capture.mu.Unlock()
			return out
		}
		capture.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	out := make([]CaptureInApi, len(capture.events))
	copy(out, capture.events)
	return out
}

func findEvent(events []CaptureInApi, eventName, flagKey string) *CaptureInApi {
	for i := range events {
		if events[i].Event != eventName {
			continue
		}
		if flagKey == "" {
			return &events[i]
		}
		if events[i].Properties["$feature_flag"] == flagKey {
			return &events[i]
		}
	}
	return nil
}

func TestEvaluateFlags_ReturnsSnapshotWithSingleRequest(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, _ := newEvalClient(t, fs.server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags returned error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if fs.callCount() != 1 {
		t.Fatalf("expected exactly 1 /flags request, got %d", fs.callCount())
	}
	keys := snap.Keys()
	if len(keys) == 0 {
		t.Fatal("expected at least one flag key")
	}
}

func TestEvaluateFlags_NoEventsUntilAccessed(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	if _, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"}); err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for _, ev := range capture.events {
		if ev.Event == "$feature_flag_called" {
			t.Fatalf("did not expect any $feature_flag_called events, got %+v", ev)
		}
	}
}

func TestIsEnabled_FiresEventWithFullMetadataAndDedupes(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	if !snap.IsEnabled("enabled-flag") {
		t.Fatal("expected enabled-flag to be enabled")
	}
	// Second call should be deduped.
	snap.IsEnabled("enabled-flag")

	events := waitForEventCount(capture, 1, 5*time.Second)
	matching := 0
	var event *CaptureInApi
	for i := range events {
		if events[i].Event == "$feature_flag_called" && events[i].Properties["$feature_flag"] == "enabled-flag" {
			matching++
			event = &events[i]
		}
	}
	if matching != 1 {
		t.Fatalf("expected exactly 1 $feature_flag_called for enabled-flag, got %d", matching)
	}
	if event.Properties["$feature_flag_response"] != true {
		t.Errorf("expected $feature_flag_response=true, got %v", event.Properties["$feature_flag_response"])
	}
	if event.Properties["$feature_flag_id"] != 1 {
		t.Errorf("expected $feature_flag_id=1, got %v", event.Properties["$feature_flag_id"])
	}
	if event.Properties["$feature_flag_version"] != 23 {
		t.Errorf("expected $feature_flag_version=23, got %v", event.Properties["$feature_flag_version"])
	}
	if event.Properties["$feature_flag_reason"] != "Matched conditions set 3" {
		t.Errorf("expected reason 'Matched conditions set 3', got %v", event.Properties["$feature_flag_reason"])
	}
	if event.Properties["$feature_flag_request_id"] != "42853c54-1431-4861-996e-3a548989fa2c" {
		t.Errorf("expected $feature_flag_request_id, got %v", event.Properties["$feature_flag_request_id"])
	}
	if event.Properties["$feature/enabled-flag"] != true {
		t.Errorf("expected $feature/enabled-flag=true, got %v", event.Properties["$feature/enabled-flag"])
	}
	if got := event.Properties["$feature_flag_payload"]; got != `{"foo": 1}` {
		t.Errorf("expected $feature_flag_payload to be the raw JSON payload, got %v", got)
	}
}

func TestGetFlag_FiresEventWithVariant(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	val := snap.GetFlag("multi-variate-flag")
	if val != "hello" {
		t.Errorf("expected variant 'hello', got %v", val)
	}

	events := waitForEventCount(capture, 1, 5*time.Second)
	event := findEvent(events, "$feature_flag_called", "multi-variate-flag")
	if event == nil {
		t.Fatal("expected $feature_flag_called event for multi-variate-flag")
	}
	if event.Properties["$feature_flag_response"] != "hello" {
		t.Errorf("expected $feature_flag_response='hello', got %v", event.Properties["$feature_flag_response"])
	}
}

func TestGetFlagPayload_DoesNotFireEvent(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	payload := snap.GetFlagPayload("simple-flag")
	if payload == "" {
		t.Errorf("expected non-empty payload for simple-flag, got %q", payload)
	}

	time.Sleep(50 * time.Millisecond)
	capture.mu.Lock()
	for _, ev := range capture.events {
		if ev.Event == "$feature_flag_called" {
			capture.mu.Unlock()
			t.Fatalf("GetFlagPayload should not fire $feature_flag_called, got %+v", ev)
		}
	}
	capture.mu.Unlock()

	// And the access set should not have recorded simple-flag — OnlyAccessed
	// must return an empty snapshot, since GetFlagPayload doesn't count as an
	// access.
	if filtered := snap.OnlyAccessed(); len(filtered.Keys()) != 0 {
		t.Errorf("OnlyAccessed should be empty when GetFlagPayload was the only call; got keys=%v", filtered.Keys())
	}
}

func TestOnlyAccessed_FiltersToAccessedFlags(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, _ := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	snap.IsEnabled("enabled-flag")
	snap.GetFlag("multi-variate-flag")

	filtered := snap.OnlyAccessed()
	keys := filtered.Keys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 accessed keys, got %d (%v)", len(keys), keys)
	}
	wantSet := map[string]bool{"enabled-flag": true, "multi-variate-flag": true}
	for _, k := range keys {
		if !wantSet[k] {
			t.Errorf("unexpected key in OnlyAccessed: %s", k)
		}
	}
}

func TestOnlyAccessed_ReturnsEmptyWhenNoFlagsAccessed(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, logger := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	filtered := snap.OnlyAccessed()
	if got := len(filtered.Keys()); got != 0 {
		t.Errorf("expected empty snapshot when no flags accessed, got %d keys", got)
	}

	for _, w := range logger.snapshot() {
		if strings.Contains(w, "OnlyAccessed") {
			t.Errorf("expected no warning for empty OnlyAccessed; method honors its name: %q", w)
		}
	}
}

func TestOnly_DropsUnknownKeysWithWarning(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, logger := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	filtered := snap.Only([]string{"enabled-flag", "totally-missing"})
	keys := filtered.Keys()
	if len(keys) != 1 || keys[0] != "enabled-flag" {
		t.Errorf("expected only enabled-flag, got %v", keys)
	}

	warns := logger.snapshot()
	found := false
	for _, w := range warns {
		if strings.Contains(w, "totally-missing") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning naming totally-missing, got %v", warns)
	}
}

func TestFilteredSnapshots_DoNotBackPropagateAccess(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, _ := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	snap.IsEnabled("enabled-flag")

	child := snap.OnlyAccessed()
	// Access another flag on the child only.
	child.IsEnabled("disabled-flag")

	parentFiltered := snap.OnlyAccessed()
	if len(parentFiltered.Keys()) != 1 || parentFiltered.Keys()[0] != "enabled-flag" {
		t.Errorf("expected parent OnlyAccessed to still hold only enabled-flag, got %v", parentFiltered.Keys())
	}
}

func TestCaptureWithFlags_AttachesPropertiesNoExtraRequest(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err := client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "thing-happened",
		Flags:      snap,
	}); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var thing *CaptureInApi
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		for i := range capture.events {
			if capture.events[i].Event == "thing-happened" {
				thing = &capture.events[i]
				break
			}
		}
		capture.mu.Unlock()
		if thing != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if thing == nil {
		t.Fatal("did not receive thing-happened event")
	}

	if thing.Properties["$feature/enabled-flag"] != true {
		t.Errorf("expected $feature/enabled-flag=true, got %v", thing.Properties["$feature/enabled-flag"])
	}
	if thing.Properties["$feature/multi-variate-flag"] != "hello" {
		t.Errorf("expected $feature/multi-variate-flag='hello', got %v", thing.Properties["$feature/multi-variate-flag"])
	}
	active, ok := thing.Properties["$active_feature_flags"].([]string)
	if !ok {
		t.Fatalf("expected $active_feature_flags to be []string, got %T", thing.Properties["$active_feature_flags"])
	}
	for i := 1; i < len(active); i++ {
		if active[i-1] > active[i] {
			t.Errorf("$active_feature_flags should be sorted, got %v", active)
		}
	}

	// Critically: only one /flags request (from EvaluateFlags), none from the
	// Capture path.
	if got := fs.callCount(); got != 1 {
		t.Errorf("expected exactly 1 /flags request, got %d", got)
	}
}

func TestEvaluateFlags_ForwardsFlagKeys(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, _ := newEvalClient(t, fs.server)

	if _, err := client.EvaluateFlags(EvaluateFlagsPayload{
		DistinctId: "user-1",
		FlagKeys:   []string{"enabled-flag", "disabled-flag"},
	}); err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	parsed, raw := fs.lastCall()
	if !strings.Contains(raw, "flag_keys_to_evaluate") {
		t.Errorf("expected raw body to contain flag_keys_to_evaluate, got %s", raw)
	}
	if len(parsed.FlagKeysToEvaluate) != 2 {
		t.Fatalf("expected 2 flag keys, got %v", parsed.FlagKeysToEvaluate)
	}
	if parsed.FlagKeysToEvaluate[0] != "enabled-flag" || parsed.FlagKeysToEvaluate[1] != "disabled-flag" {
		t.Errorf("unexpected flag keys: %v", parsed.FlagKeysToEvaluate)
	}
}

func TestEvaluateFlags_EmptyDistinctId_NoEvents(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			calls.Add(1)
			w.Write([]byte(fixture("test-flags-v4.json")))
		}
	}))
	defer server.Close()

	client, capture, _ := newEvalClient(t, server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: ""})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected zero /flags requests for empty distinct_id, got %d", got)
	}

	// Accessing a flag on an empty snapshot should be safe and silent.
	snap.IsEnabled("enabled-flag")
	snap.GetFlag("multi-variate-flag")

	time.Sleep(50 * time.Millisecond)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for _, ev := range capture.events {
		if ev.Event == "$feature_flag_called" {
			t.Fatalf("empty-distinct_id snapshot should not fire $feature_flag_called, got %+v", ev)
		}
	}
}

func TestEvaluateFlags_LocalEvaluation_TagsLocallyEvaluated(t *testing.T) {
	t.Parallel()
	var localCalls atomic.Int32
	var remoteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions") || strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			localCalls.Add(1)
			w.Write([]byte(fixture("feature_flag/test-simple-flag-person-prop.json")))
		case strings.HasPrefix(r.URL.Path, "/flags"):
			remoteCalls.Add(1)
			w.Write([]byte(fixture("test-flags-v4.json")))
		}
	}))
	defer server.Close()

	client, capture, _ := newEvalClient(t, server, func(c *Config) {
		c.PersonalApiKey = "personal-key"
	})

	// Wait for the poller's first fetch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && localCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if localCalls.Load() == 0 {
		t.Fatal("local evaluation poller never called /flags/definitions")
	}

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{
		DistinctId:       "user-1",
		PersonProperties: NewProperties().Set("region", "USA"),
	})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}
	if !snap.IsEnabled("simple-flag") {
		t.Fatal("expected simple-flag to be enabled locally")
	}

	events := waitForEventCount(capture, 1, 5*time.Second)
	event := findEvent(events, "$feature_flag_called", "simple-flag")
	if event == nil {
		t.Fatal("expected $feature_flag_called for simple-flag")
	}
	if event.Properties["locally_evaluated"] != true {
		t.Errorf("expected locally_evaluated=true, got %v", event.Properties["locally_evaluated"])
	}
	if event.Properties["$feature_flag_reason"] != "Evaluated locally" {
		t.Errorf("expected $feature_flag_reason='Evaluated locally', got %v", event.Properties["$feature_flag_reason"])
	}
}

// silentLogger drops all log calls. Callers who want to silence
// FeatureFlagEvaluations filter-helper warnings can pass one as Config.Logger.
type silentLogger struct{}

func (silentLogger) Debugf(string, ...interface{}) {}
func (silentLogger) Logf(string, ...interface{})   {}
func (silentLogger) Warnf(string, ...interface{})  {}
func (silentLogger) Errorf(string, ...interface{}) {}

func TestFilterWarnings_SilencedByQuietLogger(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	// Override Logger entirely so Only's warning has nowhere to go.
	cfg := Config{
		Endpoint:  fs.server.URL,
		BatchSize: 1,
		Logger:    silentLogger{},
	}
	cli, err := NewWithConfig("test-api-key", cfg)
	if err != nil {
		t.Fatalf("NewWithConfig failed: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	snap, _ := cli.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	// Should not panic and should not log anywhere observable.
	_ = snap.OnlyAccessed()
	_ = snap.Only([]string{"definitely-not-here"})
}

func TestRefactor_LegacyAndSnapshotPathsDedupeIdentically(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	// Legacy single-flag path fires once.
	if _, err := client.IsFeatureEnabled(FeatureFlagPayload{Key: "enabled-flag", DistinctId: "user-1"}); err != nil {
		t.Fatalf("IsFeatureEnabled error: %v", err)
	}

	// Snapshot path with the same (distinct_id, key, response) must NOT fire
	// a second event because the LRU cache is shared.
	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	snap.IsEnabled("enabled-flag")

	time.Sleep(150 * time.Millisecond)

	capture.mu.Lock()
	defer capture.mu.Unlock()
	count := 0
	for _, ev := range capture.events {
		if ev.Event == "$feature_flag_called" && ev.Properties["$feature_flag"] == "enabled-flag" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 deduped $feature_flag_called event, got %d", count)
	}
}

func TestErrorsWhileComputingFlags_PropagatesToEvent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			w.Write([]byte(`{
				"flags": {
					"enabled-flag": {
						"key": "enabled-flag",
						"enabled": true,
						"variant": null,
						"reason": null,
						"metadata": {"id": 1, "version": 2, "payload": null}
					}
				},
				"requestId": "req-err-1",
				"errorsWhileComputingFlags": true
			}`))
		}
	}))
	defer server.Close()

	client, capture, _ := newEvalClient(t, server)

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err != nil {
		t.Fatalf("EvaluateFlags error: %v", err)
	}

	// Known flag — only the response-level error should be emitted.
	snap.IsEnabled("enabled-flag")
	// Missing flag — both errors must be combined comma-joined.
	snap.IsEnabled("missing-flag")

	events := waitForEventCount(capture, 2, 5*time.Second)

	known := findEvent(events, "$feature_flag_called", "enabled-flag")
	if known == nil {
		t.Fatal("expected event for enabled-flag")
	}
	if got := known.Properties["$feature_flag_error"]; got != FeatureFlagErrorErrorsWhileComputing {
		t.Errorf("expected $feature_flag_error=%q for known flag, got %v", FeatureFlagErrorErrorsWhileComputing, got)
	}

	missing := findEvent(events, "$feature_flag_called", "missing-flag")
	if missing == nil {
		t.Fatal("expected event for missing-flag")
	}
	wantCombined := FeatureFlagErrorErrorsWhileComputing + "," + FeatureFlagErrorFlagMissing
	if got := missing.Properties["$feature_flag_error"]; got != wantCombined {
		t.Errorf("expected combined $feature_flag_error=%q, got %v", wantCombined, got)
	}
}

func TestCaptureWarnsAndUsesFlagsWhenBothFlagsAndSendFeatureFlagsSet(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, _, logger := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	callsBefore := fs.callCount()

	if err := client.Enqueue(Capture{
		DistinctId:       "user-1",
		Event:            "thing-happened",
		Flags:            snap,
		SendFeatureFlags: SendFeatureFlags(true),
	}); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if got := fs.callCount(); got != callsBefore {
		t.Errorf("Flags must take precedence and avoid a second /flags request; got %d new calls", got-callsBefore)
	}

	found := false
	for _, w := range logger.snapshot() {
		if strings.Contains(w, "Both Flags and SendFeatureFlags") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about precedence, got %v", logger.snapshot())
	}
}

func TestEvaluateFlags_RemoteErrorReturnsPartialLocalSnapshot(t *testing.T) {
	t.Parallel()
	// Mixed-resolution fixture: beta-feature resolves locally (rollout 100%),
	// beta-feature2 needs a `country` property the caller doesn't supply so
	// the poller can't compute it locally → falls back to remote. The remote
	// /flags request 500s. The snapshot must still carry beta-feature so the
	// caller doesn't lose the locally-resolved work.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/flags/definitions") || strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
			w.Write([]byte(fixture("feature_flag/test-get-all-flags-with-fallback-but-only-local-evaluation-set.json")))
		case strings.HasPrefix(r.URL.Path, "/flags"):
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client, _, _ := newEvalClient(t, server, func(c *Config) {
		c.PersonalApiKey = "personal-key"
	})

	// Wait for the poller's first definitions fetch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		flags, _ := client.GetFeatureFlags()
		if len(flags) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	snap, err := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})
	if err == nil {
		t.Fatal("expected non-nil error from failing /flags request")
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot even when remote /flags errors — local results should not be lost")
	}
	if !snap.IsEnabled("beta-feature") {
		t.Errorf("expected beta-feature to be resolved locally despite remote error; got keys=%v", snap.Keys())
	}
}

func TestCaptureWithFlags_UserPropertiesOverrideGenerated(t *testing.T) {
	t.Parallel()
	fs := newFlagsServer(t, "test-flags-v4.json")
	defer fs.close()

	client, capture, _ := newEvalClient(t, fs.server)

	snap, _ := client.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-1"})

	// Caller explicitly overrides the auto-generated $feature/enabled-flag
	// property. Their value must win — generated props are merged first.
	overrides := NewProperties().
		Set("$feature/enabled-flag", "user-override").
		Set("custom-prop", 42)

	if err := client.Enqueue(Capture{
		DistinctId: "user-1",
		Event:      "thing-happened",
		Properties: overrides,
		Flags:      snap,
	}); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var event *CaptureInApi
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		for i := range capture.events {
			if capture.events[i].Event == "thing-happened" {
				event = &capture.events[i]
				break
			}
		}
		capture.mu.Unlock()
		if event != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if event == nil {
		t.Fatal("did not receive thing-happened event")
	}

	if got := event.Properties["$feature/enabled-flag"]; got != "user-override" {
		t.Errorf("expected user-supplied $feature/enabled-flag override to win, got %v", got)
	}
	if got := event.Properties["custom-prop"]; got != 42 {
		t.Errorf("expected custom-prop=42 to be preserved, got %v", got)
	}
	// Other generated flag properties from the snapshot must still be present.
	if event.Properties["$feature/multi-variate-flag"] != "hello" {
		t.Errorf("expected non-overridden $feature/multi-variate-flag to remain 'hello', got %v", event.Properties["$feature/multi-variate-flag"])
	}
}
