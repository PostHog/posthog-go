package posthog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	json "github.com/goccy/go-json"
)

// TestScenario defines common test scenarios for transport mocking
type TestScenario int

const (
	ScenarioOK TestScenario = iota
	ScenarioDelayed
	ScenarioBadRequest
	ScenarioBodyError
	ScenarioNetworkError
	ScenarioServerError
)

// UnifiedCallback is a comprehensive callback mock with channels for synchronization
type UnifiedCallback struct {
	t            *testing.T
	mu           sync.Mutex
	successCount int
	failureCount int
	lastError    error
	SuccessChan  chan APIMessage
	FailureChan  chan error
}

// NewUnifiedCallback creates a new unified callback for testing
func NewUnifiedCallback(t *testing.T) *UnifiedCallback {
	return &UnifiedCallback{
		t:           t,
		SuccessChan: make(chan APIMessage, 100),
		FailureChan: make(chan error, 100),
	}
}

func (c *UnifiedCallback) Success(msg APIMessage) {
	c.mu.Lock()
	c.successCount++
	c.mu.Unlock()
	if c.t != nil {
		c.t.Logf("Callback: SUCCESS")
	}
	select {
	case c.SuccessChan <- msg:
	default:
	}
}

func (c *UnifiedCallback) Failure(msg APIMessage, err error) {
	c.mu.Lock()
	c.failureCount++
	c.lastError = err
	c.mu.Unlock()
	if c.t != nil {
		c.t.Logf("Callback: FAILURE - %v", err)
	}
	select {
	case c.FailureChan <- err:
	default:
	}
}

func (c *UnifiedCallback) GetCounts() (success, failure int) { return c.counts() }

func (c *UnifiedCallback) counts() (success, failure int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount
}

func (c *UnifiedCallback) GetLastError() error { return c.lastErr() }

func (c *UnifiedCallback) lastErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastError
}

// MockServerConfig configures a mock HTTP server for testing
type MockServerConfig struct {
	// Response configurations
	FlagsResponse     string
	FlagsStatusCode   int
	LocalEvalResponse string
	LocalEvalStatus   int

	// Behavior modifiers
	LatencyMs  int
	FailAfterN int

	// Handlers for custom behavior
	FlagsHandler     func(w http.ResponseWriter, r *http.Request)
	LocalEvalHandler func(w http.ResponseWriter, r *http.Request)
	// CaptureHandler handles the capture endpoint, returning (status, body).
	// When nil, the server replies 200 with an all-"ok" results map derived from
	// the request batch.
	CaptureHandler func(body []byte) (int, string)
}

// MockServerBuilder builds configurable mock servers
type MockServerBuilder struct {
	config       MockServerConfig
	requestCount int
	paths        []string
	mu           sync.Mutex
}

// NewMockServerBuilder creates a new mock server builder with defaults
func NewMockServerBuilder() *MockServerBuilder {
	return &MockServerBuilder{
		config: MockServerConfig{
			FlagsStatusCode: http.StatusOK,
			LocalEvalStatus: http.StatusOK,
		},
	}
}

func (b *MockServerBuilder) WithFlagsResponse(response string, statusCode int) *MockServerBuilder {
	return b.withResponse(response, statusCode, &b.config.FlagsResponse, &b.config.FlagsStatusCode)
}

func (b *MockServerBuilder) WithLocalEvalResponse(response string, statusCode int) *MockServerBuilder {
	return b.withResponse(response, statusCode, &b.config.LocalEvalResponse, &b.config.LocalEvalStatus)
}

func (b *MockServerBuilder) withResponse(response string, statusCode int, responseField *string, statusField *int) *MockServerBuilder {
	*responseField = response
	*statusField = statusCode
	return b
}

func (b *MockServerBuilder) WithCaptureHandler(handler func(body []byte) (int, string)) *MockServerBuilder {
	b.config.CaptureHandler = handler
	return b
}

// GetPaths returns the request paths the server has seen, in order.
func (b *MockServerBuilder) GetPaths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.paths))
	copy(out, b.paths)
	return out
}

func (b *MockServerBuilder) WithFailAfterN(n int) *MockServerBuilder {
	b.config.FailAfterN = n
	return b
}

func (b *MockServerBuilder) GetRequestCount() int { return b.requestCountLocked() }

func (b *MockServerBuilder) requestCountLocked() int {
	b.mu.Lock()
	count := b.requestCount
	b.mu.Unlock()
	return count
}

func (b *MockServerBuilder) Build() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.requestCount++
		count := b.requestCount
		b.paths = append(b.paths, r.URL.Path)
		b.mu.Unlock()

		// Check if we should fail
		if b.config.FailAfterN > 0 && count > b.config.FailAfterN {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Route to appropriate handler
		switch {
		case strings.HasPrefix(r.URL.Path, capturePath):
			body, _ := io.ReadAll(r.Body)
			status, resp := http.StatusOK, ""
			if b.config.CaptureHandler != nil {
				status, resp = b.config.CaptureHandler(body)
			} else {
				status, resp = http.StatusOK, allOkResultsBody(body)
			}
			w.WriteHeader(status)
			if resp != "" {
				w.Write([]byte(resp))
			}

		case r.URL.Path == "/flags" || r.URL.Path == "/flags/":
			if b.config.FlagsHandler != nil {
				b.config.FlagsHandler(w, r)
				return
			}
			w.WriteHeader(b.config.FlagsStatusCode)
			if b.config.FlagsResponse != "" {
				w.Write([]byte(b.config.FlagsResponse))
			}

		case strings.HasPrefix(r.URL.Path, "/flags/definitions"):
			if b.config.LocalEvalHandler != nil {
				b.config.LocalEvalHandler(w, r)
				return
			}
			w.WriteHeader(b.config.LocalEvalStatus)
			if b.config.LocalEvalResponse != "" {
				w.Write([]byte(b.config.LocalEvalResponse))
			}

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// allOkResultsBody parses a capture request envelope and returns a results
// body marking every event uuid as "ok".
func allOkResultsBody(body []byte) string {
	var env eventBatch
	if err := json.Unmarshal(body, &env); err != nil {
		return `{"results":{}}`
	}
	results := map[string]eventResult{}
	for _, raw := range env.Batch {
		var ev struct {
			Uuid string `json:"uuid"`
		}
		_ = json.Unmarshal(raw, &ev)
		results[ev.Uuid] = eventResult{Result: resultOk}
	}
	out, _ := json.Marshal(captureResponse{Results: results})
	return string(out)
}

// writeCaptureOK answers a mock capture request with an all-"ok" results body.
// A 200 whose body is not a results map is terminal, so a bare WriteHeader(200)
// drops every event.
func writeCaptureOK(w http.ResponseWriter, requestBody []byte) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(allOkResultsBody(requestBody)))
}

// serveCaptureOK handles a capture request, reporting whether it did. Call it
// first in flag-focused servers, which also receive $feature_flag_called events.
func serveCaptureOK(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, capturePath) {
		return false
	}
	body, _ := io.ReadAll(r.Body)
	writeCaptureOK(w, body)
	return true
}

// NewTestTransport creates a transport for the given test scenario
func NewTestTransport(scenario TestScenario) http.RoundTripper {
	switch scenario {
	case ScenarioOK:
		return testTransportOK
	case ScenarioDelayed:
		return testTransportDelayed
	case ScenarioBadRequest:
		return testTransportBadRequest
	case ScenarioBodyError:
		return testTransportBodyError
	case ScenarioNetworkError:
		return testTransportError
	case ScenarioServerError:
		return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				Status:     http.StatusText(http.StatusInternalServerError),
				StatusCode: http.StatusInternalServerError,
				Proto:      r.Proto,
				ProtoMajor: r.ProtoMajor,
				ProtoMinor: r.ProtoMinor,
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		})
	default:
		return testTransportOK
	}
}

// NoOpTransport returns a transport that succeeds immediately without any network calls
// Useful for benchmarking enqueue throughput without HTTP overhead
func NoOpTransport() http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		return &http.Response{
			Status:     http.StatusText(http.StatusOK),
			StatusCode: http.StatusOK,
			Proto:      r.Proto,
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})
}
