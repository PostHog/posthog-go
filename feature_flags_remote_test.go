package posthog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetFeatureFlagFromRemote(t *testing.T) {
	t.Run("returns flag value when flag exists", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"test-flag": {
						"key": "test-flag",
						"enabled": true,
						"variant": null
					}
				},
				"requestId": "req-123",
				"evaluatedAt": 1737312368000
			}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err != nil {
			t.Errorf("Expected no error, got: %v", result.Err)
		}
		if result.FlagMissing {
			t.Error("Expected FlagMissing to be false")
		}
		if result.QuotaLimited {
			t.Error("Expected QuotaLimited to be false")
		}
		if result.RequestID == nil || *result.RequestID != "req-123" {
			t.Errorf("Expected RequestID to be 'req-123', got: %v", result.RequestID)
		}
		if result.EvaluatedAt == nil || *result.EvaluatedAt != 1737312368000 {
			t.Errorf("Expected EvaluatedAt to be 1737312368000, got: %v", result.EvaluatedAt)
		}
	})

	t.Run("returns multivariate flag value", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"variant-flag": {
						"key": "variant-flag",
						"enabled": true,
						"variant": "control"
					}
				},
				"requestId": "req-456"
			}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("variant-flag", "user-123", nil, nil, nil)

		if result.Err != nil {
			t.Errorf("Expected no error, got: %v", result.Err)
		}
		if result.FlagDetail == nil {
			t.Error("Expected FlagDetail to be set")
		}
	})

	t.Run("returns false with FlagMissing when flag not in response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {},
				"requestId": "req-789"
			}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("missing-flag", "user-123", nil, nil, nil)

		if result.Err != nil {
			t.Errorf("Expected no error, got: %v", result.Err)
		}
		if result.Value != false {
			t.Errorf("Expected Value to be false, got: %v", result.Value)
		}
		if !result.FlagMissing {
			t.Error("Expected FlagMissing to be true")
		}
	})

	t.Run("returns false with QuotaLimited when quota limited", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"test-flag": {
						"key": "test-flag",
						"enabled": true
					}
				},
				"quota_limited": ["feature_flags"],
				"requestId": "req-quota"
			}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err != nil {
			t.Errorf("Expected no error, got: %v", result.Err)
		}
		if result.Value != false {
			t.Errorf("Expected Value to be false when quota limited, got: %v", result.Value)
		}
		if !result.QuotaLimited {
			t.Error("Expected QuotaLimited to be true")
		}
	})

	t.Run("sets ErrorsWhileComputingFlags when server reports errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{
				"flags": {
					"test-flag": {
						"key": "test-flag",
						"enabled": true
					}
				},
				"errorsWhileComputingFlags": true,
				"requestId": "req-errors"
			}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err != nil {
			t.Errorf("Expected no error, got: %v", result.Err)
		}
		if !result.ErrorsWhileComputingFlags {
			t.Error("Expected ErrorsWhileComputingFlags to be true")
		}
	})

	t.Run("returns APIError with status code when server returns non-OK status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err == nil {
			t.Error("Expected an error for failed HTTP request")
		}
		if result.Value != false {
			t.Errorf("Expected Value to be false on error, got: %v", result.Value)
		}

		// Verify it's an APIError that classifies correctly
		errorString := result.GetErrorString()
		if errorString != "api_error_500" {
			t.Errorf("Expected error string 'api_error_500', got: %s", errorString)
		}
	})

	t.Run("returns APIError with status 401 for unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err == nil {
			t.Error("Expected an error for unauthorized request")
		}

		errorString := result.GetErrorString()
		if errorString != "api_error_401" {
			t.Errorf("Expected error string 'api_error_401', got: %s", errorString)
		}
	})

	t.Run("returns error when server returns invalid JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`not valid json`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err == nil {
			t.Error("Expected an error for invalid JSON response")
		}
	})

	t.Run("returns error when server is unreachable", func(t *testing.T) {
		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: "http://localhost:1", // Unreachable port
		})
		defer posthog.Close()

		c := posthog.(*client)
		result := c.getFeatureFlagFromRemote("test-flag", "user-123", nil, nil, nil)

		if result.Err == nil {
			t.Error("Expected an error when server is unreachable")
		}
		if result.Value != false {
			t.Errorf("Expected Value to be false on error, got: %v", result.Value)
		}
	})

	t.Run("GetErrorString returns correct error strings", func(t *testing.T) {
		tests := []struct {
			name     string
			result   FeatureFlagResult
			expected string
		}{
			{
				name:     "no errors",
				result:   FeatureFlagResult{Value: true},
				expected: "",
			},
			{
				name:     "flag missing",
				result:   FeatureFlagResult{Value: false, FlagMissing: true},
				expected: "flag_missing",
			},
			{
				name:     "quota limited",
				result:   FeatureFlagResult{Value: false, QuotaLimited: true},
				expected: "quota_limited",
			},
			{
				name:     "errors while computing",
				result:   FeatureFlagResult{Value: true, ErrorsWhileComputingFlags: true},
				expected: "errors_while_computing_flags",
			},
			{
				name:     "multiple errors",
				result:   FeatureFlagResult{Value: false, QuotaLimited: true, FlagMissing: true},
				expected: "quota_limited,flag_missing",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.result.GetErrorString()
				if got != tt.expected {
					t.Errorf("GetErrorString() = %q, want %q", got, tt.expected)
				}
			})
		}
	})

	t.Run("passes person properties in request", func(t *testing.T) {
		var receivedBody string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			receivedBody = string(buf[:n])
			w.Write([]byte(`{"flags": {}, "requestId": "req-props"}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		personProps := NewProperties().Set("email", "test@example.com")
		c.getFeatureFlagFromRemote("test-flag", "user-123", nil, personProps, nil)

		if !strings.Contains(receivedBody, "test@example.com") {
			t.Errorf("Expected request body to contain person properties, got: %s", receivedBody)
		}
	})

	t.Run("passes groups in request", func(t *testing.T) {
		var receivedBody string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			receivedBody = string(buf[:n])
			w.Write([]byte(`{"flags": {}, "requestId": "req-groups"}`))
		}))
		defer server.Close()

		posthog, _ := NewWithConfig("test-api-key", Config{
			Endpoint: server.URL,
		})
		defer posthog.Close()

		c := posthog.(*client)
		groups := Groups{"company": "posthog"}
		c.getFeatureFlagFromRemote("test-flag", "user-123", groups, nil, nil)

		if !strings.Contains(receivedBody, "posthog") {
			t.Errorf("Expected request body to contain groups, got: %s", receivedBody)
		}
	})
}

// mockNetError implements net.Error for testing
type mockNetError struct {
	timeout   bool
	temporary bool
	message   string
}

func (e *mockNetError) Error() string   { return e.message }
func (e *mockNetError) Timeout() bool   { return e.timeout }
func (e *mockNetError) Temporary() bool { return e.temporary }

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error returns empty string",
			err:      nil,
			expected: "",
		},
		{
			name:     "context.DeadlineExceeded returns timeout",
			err:      context.DeadlineExceeded,
			expected: FeatureFlagErrorTimeout,
		},
		{
			name:     "wrapped context.DeadlineExceeded returns timeout",
			err:      fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			expected: FeatureFlagErrorTimeout,
		},
		{
			name:     "context.Canceled returns timeout",
			err:      context.Canceled,
			expected: FeatureFlagErrorTimeout,
		},
		{
			name:     "wrapped context.Canceled returns timeout",
			err:      fmt.Errorf("request canceled: %w", context.Canceled),
			expected: FeatureFlagErrorTimeout,
		},
		{
			name:     "net.Error with Timeout() true returns timeout",
			err:      &mockNetError{timeout: true, message: "i/o timeout"},
			expected: FeatureFlagErrorTimeout,
		},
		{
			name:     "net.Error without timeout returns connection_error",
			err:      &mockNetError{timeout: false, message: "connection reset by peer"},
			expected: FeatureFlagErrorConnectionError,
		},
		{
			name:     "DNS error returns connection_error",
			err:      &net.DNSError{Err: "no such host", Name: "example.com"},
			expected: FeatureFlagErrorConnectionError,
		},
		{
			name:     "wrapped DNS error returns connection_error",
			err:      fmt.Errorf("lookup failed: %w", &net.DNSError{Err: "no such host", Name: "example.com"}),
			expected: FeatureFlagErrorConnectionError,
		},
		{
			name:     "net.OpError returns connection_error",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			expected: FeatureFlagErrorConnectionError,
		},
		{
			name:     "wrapped net.OpError returns connection_error",
			err:      fmt.Errorf("connect failed: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}),
			expected: FeatureFlagErrorConnectionError,
		},
		{
			name:     "generic error returns unknown_error",
			err:      errors.New("something went wrong"),
			expected: FeatureFlagErrorUnknownError,
		},
		{
			name:     "APIError with status 500 returns api_error_500",
			err:      NewAPIError(500, "internal server error"),
			expected: "api_error_500",
		},
		{
			name:     "APIError with status 401 returns api_error_401",
			err:      NewAPIError(401, "unauthorized"),
			expected: "api_error_401",
		},
		{
			name:     "APIError with status 404 returns api_error_404",
			err:      NewAPIError(404, "not found"),
			expected: "api_error_404",
		},
		{
			name:     "wrapped APIError returns api_error_{status}",
			err:      fmt.Errorf("request failed: %w", NewAPIError(503, "service unavailable")),
			expected: "api_error_503",
		},
		{
			name:     "error with timeout in message returns unknown_error (no string matching)",
			err:      errors.New("request timeout after 30s"),
			expected: FeatureFlagErrorUnknownError,
		},
		{
			name:     "error with connection refused in message returns unknown_error (no string matching)",
			err:      errors.New("dial tcp 127.0.0.1:1: connection refused"),
			expected: FeatureFlagErrorUnknownError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.err)
			if got != tt.expected {
				t.Errorf("classifyError() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetErrorStringWithRequestErrors(t *testing.T) {
	tests := []struct {
		name     string
		result   FeatureFlagResult
		expected string
	}{
		{
			name: "timeout error only",
			result: FeatureFlagResult{
				Value: false,
				Err:   context.DeadlineExceeded,
			},
			expected: "timeout",
		},
		{
			name: "connection error only",
			result: FeatureFlagResult{
				Value: false,
				Err:   &net.DNSError{Err: "no such host", Name: "example.com"},
			},
			expected: "connection_error",
		},
		{
			name: "unknown error only",
			result: FeatureFlagResult{
				Value: false,
				Err:   errors.New("something went wrong"),
			},
			expected: "unknown_error",
		},
		{
			name: "request error combined with errors_while_computing_flags",
			result: FeatureFlagResult{
				Value:                     false,
				Err:                       errors.New("something went wrong"),
				ErrorsWhileComputingFlags: true,
			},
			expected: "unknown_error,errors_while_computing_flags",
		},
		{
			name: "timeout combined with quota_limited",
			result: FeatureFlagResult{
				Value:        false,
				Err:          context.DeadlineExceeded,
				QuotaLimited: true,
			},
			expected: "timeout,quota_limited",
		},
		{
			name: "connection error combined with flag_missing",
			result: FeatureFlagResult{
				Value:       false,
				Err:         &net.DNSError{Err: "no such host", Name: "example.com"},
				FlagMissing: true,
			},
			expected: "connection_error,flag_missing",
		},
		{
			name: "all error types combined",
			result: FeatureFlagResult{
				Value:                     false,
				Err:                       errors.New("request failed"),
				ErrorsWhileComputingFlags: true,
				QuotaLimited:              true,
				FlagMissing:               true,
			},
			expected: "unknown_error,errors_while_computing_flags,quota_limited,flag_missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.GetErrorString()
			if got != tt.expected {
				t.Errorf("GetErrorString() = %q, want %q", got, tt.expected)
			}
		})
	}
}
