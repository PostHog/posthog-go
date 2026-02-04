package posthog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

// ErrFlagNotFound is returned when a feature flag does not exist or is disabled.
// Use errors.Is(err, ErrFlagNotFound) to check for this error.
var ErrFlagNotFound = errors.New("feature flag not found")

// Feature flag error type constants for the $feature_flag_error property.
// These values are sent in analytics events to track flag evaluation failures.
// They should not be changed without considering impact on existing dashboards
// and queries that filter on these values.
const (
	// FeatureFlagErrorErrorsWhileComputing indicates the server returned errorsWhileComputingFlags=true
	FeatureFlagErrorErrorsWhileComputing = "errors_while_computing_flags"

	// FeatureFlagErrorFlagMissing indicates the requested flag was not in the API response
	FeatureFlagErrorFlagMissing = "flag_missing"

	// FeatureFlagErrorEvaluationFailed indicates the flag was present but failed to evaluate due to a transient server error
	FeatureFlagErrorEvaluationFailed = "evaluation_failed"

	// FeatureFlagErrorQuotaLimited indicates rate/quota limit was exceeded
	FeatureFlagErrorQuotaLimited = "quota_limited"

	// FeatureFlagErrorTimeout indicates request timed out
	FeatureFlagErrorTimeout = "timeout"

	// FeatureFlagErrorConnectionError indicates network connectivity issue
	FeatureFlagErrorConnectionError = "connection_error"

	// FeatureFlagErrorUnknownError indicates an unexpected error occurred
	FeatureFlagErrorUnknownError = "unknown_error"

	// FeatureFlagErrorAPIErrorPrefix is the prefix for API errors with status codes
	FeatureFlagErrorAPIErrorPrefix = "api_error_"
)

// APIError represents an HTTP API error with a status code.
// This allows classifyError to generate api_error_{status} strings.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return e.Message
}

// NewAPIError creates a new APIError with the given status code.
func NewAPIError(statusCode int, message string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
	}
}

// featureFlagEvaluationResult holds internal evaluation context
// along with any error information that occurred during evaluation.
type featureFlagEvaluationResult struct {
	Value                     interface{}
	Err                       error
	ErrorsWhileComputingFlags bool
	QuotaLimited              bool
	FlagMissing               bool
	FlagFailed                bool
	RequestID                 *string
	EvaluatedAt               *int64
	FlagDetail                *FlagDetail
}

// classifyError determines the error type string for a given error.
// Returns api_error_{status}, timeout, connection_error, or unknown_error.
//
// Classification priority:
// 1. API errors (*APIError) - returns api_error_{status}
// 2. Sentinel errors (context.DeadlineExceeded, context.Canceled)
// 3. Interface checks (net.Error, os.IsTimeout)
// 4. Concrete types (*net.DNSError, *net.OpError)
// 5. Default to unknown_error (acceptable for analytics)
func classifyError(err error) string {
	if err == nil {
		return ""
	}

	// Check API errors first (application-level errors with status codes)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("%s%d", FeatureFlagErrorAPIErrorPrefix, apiErr.StatusCode)
	}

	// Check sentinel errors (works with wrapped errors)
	if errors.Is(err, context.DeadlineExceeded) {
		return FeatureFlagErrorTimeout
	}
	if errors.Is(err, context.Canceled) {
		return FeatureFlagErrorTimeout
	}

	// Check os.IsTimeout (handles various timeout wrappers)
	if os.IsTimeout(err) {
		return FeatureFlagErrorTimeout
	}

	// Check net.Error interface
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return FeatureFlagErrorTimeout
		}
		return FeatureFlagErrorConnectionError
	}

	// Check specific network error types
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return FeatureFlagErrorConnectionError
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return FeatureFlagErrorConnectionError
	}

	// Default to unknown - this is acceptable for analytics.
	// We prefer reliable classification over broad but fragile string matching.
	return FeatureFlagErrorUnknownError
}

// GetErrorString returns the error string for the $feature_flag_error property.
// Returns empty string if there are no errors.
// Multiple errors are joined with commas.
func (r *featureFlagEvaluationResult) GetErrorString() string {
	var errorStrings []string

	// Classify request error
	if r.Err != nil {
		errorStrings = append(errorStrings, classifyError(r.Err))
	}

	// Server had errors computing flags
	if r.ErrorsWhileComputingFlags {
		errorStrings = append(errorStrings, FeatureFlagErrorErrorsWhileComputing)
	}

	// Quota limited
	if r.QuotaLimited {
		errorStrings = append(errorStrings, FeatureFlagErrorQuotaLimited)
	}

	// Flag was not in the response
	if r.FlagMissing {
		errorStrings = append(errorStrings, FeatureFlagErrorFlagMissing)
	}

	// Flag was present but failed to evaluate
	if r.FlagFailed {
		errorStrings = append(errorStrings, FeatureFlagErrorEvaluationFailed)
	}

	return strings.Join(errorStrings, ",")
}
