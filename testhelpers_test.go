package posthog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

func (c *UnifiedCallback) GetCounts() (success, failure int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount
}

func (c *UnifiedCallback) GetLastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastError
}

// MockServerConfig configures a mock HTTP server for testing
type MockServerConfig struct {
	// Response configurations
	BatchResponse     string
	BatchStatusCode   int
	FlagsResponse     string
	FlagsStatusCode   int
	LocalEvalResponse string
	LocalEvalStatus   int

	// Behavior modifiers
	LatencyMs  int
	FailAfterN int

	// Handlers for custom behavior
	BatchHandler     func(body []byte)
	FlagsHandler     func(w http.ResponseWriter, r *http.Request)
	LocalEvalHandler func(w http.ResponseWriter, r *http.Request)
}

// MockServerBuilder builds configurable mock servers
type MockServerBuilder struct {
	config       MockServerConfig
	requestCount int
	mu           sync.Mutex
}

// NewMockServerBuilder creates a new mock server builder with defaults
func NewMockServerBuilder() *MockServerBuilder {
	return &MockServerBuilder{
		config: MockServerConfig{
			BatchStatusCode: http.StatusOK,
			FlagsStatusCode: http.StatusOK,
			LocalEvalStatus: http.StatusOK,
		},
	}
}

func (b *MockServerBuilder) WithBatchResponse(response string, statusCode int) *MockServerBuilder {
	b.config.BatchResponse = response
	b.config.BatchStatusCode = statusCode
	return b
}

func (b *MockServerBuilder) WithFlagsResponse(response string, statusCode int) *MockServerBuilder {
	b.config.FlagsResponse = response
	b.config.FlagsStatusCode = statusCode
	return b
}

func (b *MockServerBuilder) WithLocalEvalResponse(response string, statusCode int) *MockServerBuilder {
	b.config.LocalEvalResponse = response
	b.config.LocalEvalStatus = statusCode
	return b
}

func (b *MockServerBuilder) WithBatchHandler(handler func(body []byte)) *MockServerBuilder {
	b.config.BatchHandler = handler
	return b
}

func (b *MockServerBuilder) WithFailAfterN(n int) *MockServerBuilder {
	b.config.FailAfterN = n
	return b
}

func (b *MockServerBuilder) GetRequestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requestCount
}

func (b *MockServerBuilder) Build() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.requestCount++
		count := b.requestCount
		b.mu.Unlock()

		// Check if we should fail
		if b.config.FailAfterN > 0 && count > b.config.FailAfterN {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Route to appropriate handler
		switch {
		case strings.HasPrefix(r.URL.Path, "/batch"):
			if b.config.BatchHandler != nil {
				body, _ := io.ReadAll(r.Body)
				b.config.BatchHandler(body)
			}
			w.WriteHeader(b.config.BatchStatusCode)
			if b.config.BatchResponse != "" {
				w.Write([]byte(b.config.BatchResponse))
			}

		case strings.HasPrefix(r.URL.Path, "/flags"):
			if b.config.FlagsHandler != nil {
				b.config.FlagsHandler(w, r)
				return
			}
			w.WriteHeader(b.config.FlagsStatusCode)
			if b.config.FlagsResponse != "" {
				w.Write([]byte(b.config.FlagsResponse))
			}

		case strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation"):
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

