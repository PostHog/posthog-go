package posthog

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// EmailErrorName is a coarse category of an EmailError, for code that branches
// on the kind of failure rather than the exact code. The names follow Resend's
// error names, so code moved from Resend keeps working.
type EmailErrorName string

const (
	// EmailErrorValidation: PostHog or the client refused the email as sent,
	// such as an unverified sender, a suppressed recipient or an argument the
	// client checks before any request.
	EmailErrorValidation EmailErrorName = "validation_error"
	// EmailErrorMissingAPIKey: the client has no secret key, or PostHog
	// refused it.
	EmailErrorMissingAPIKey EmailErrorName = "missing_api_key"
	// EmailErrorInvalidPermission: the secret key lacks the scope.
	EmailErrorInvalidPermission EmailErrorName = "invalid_permission"
	// EmailErrorNotFound: no template or email with this ID, or the API is not
	// enabled for the project.
	EmailErrorNotFound EmailErrorName = "not_found"
	// EmailErrorConcurrentIdempotentRequests: a send with the same idempotency
	// key is still in flight.
	EmailErrorConcurrentIdempotentRequests EmailErrorName = "concurrent_idempotent_requests"
	// EmailErrorInvalidIdempotentRequest: the idempotency key was used with
	// another request.
	EmailErrorInvalidIdempotentRequest EmailErrorName = "invalid_idempotent_request"
	// EmailErrorRateLimitExceeded: PostHog rate limited the request.
	EmailErrorRateLimitExceeded EmailErrorName = "rate_limit_exceeded"
	// EmailErrorMonthlyQuotaExceeded: the project is over its email quota.
	EmailErrorMonthlyQuotaExceeded EmailErrorName = "monthly_quota_exceeded"
	// EmailErrorServiceUnavailable: PostHog or the network was unavailable.
	EmailErrorServiceUnavailable EmailErrorName = "service_unavailable"
	// EmailErrorApplication: anything else.
	EmailErrorApplication EmailErrorName = "application_error"
)

// EmailError reports that PostHog did not queue the email or return its
// status, or that the client could not tell whether it did. Every error that
// Emails returns is an *EmailError; read it with errors.As.
//
// Branch on Code, PostHog's stable error code such as "recipient_suppressed",
// or an SDK code such as "network_error" or "missing_app_host". Name is its
// coarse category. The error never holds the secret key or the request body.
type EmailError struct {
	Name    EmailErrorName
	Code    string
	Message string
	// StatusCode is the HTTP status when PostHog answered, and 0 otherwise.
	StatusCode int
	Why        string
	Fix        string
	DocsURL    string
	// Field names the request field the error is about, when there is one.
	Field string
	// RetryAfter is how long PostHog asked the client to wait before it sends
	// again, and 0 when it did not ask.
	RetryAfter time.Duration

	cause error
}

func (e *EmailError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("posthog emails: %s (HTTP %d): %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("posthog emails: %s: %s", e.Code, e.Message)
}

// Unwrap returns the cause, such as a network error, context.DeadlineExceeded,
// ErrSDKDisabled or ErrClosed.
func (e *EmailError) Unwrap() error { return e.cause }

func newEmailError(code, message string, cause error) *EmailError {
	return &EmailError{Name: emailErrorName(0, code), Code: code, Message: message, cause: cause}
}

func invalidEmail(code, field, message string) *EmailError {
	err := newEmailError(code, message, nil)
	err.Field = field
	return err
}

func emailErrorName(status int, code string) EmailErrorName {
	if code == "missing_secret_key" || status == http.StatusUnauthorized {
		return EmailErrorMissingAPIKey
	}
	switch status {
	case 0:
		if code == "network_error" {
			return EmailErrorServiceUnavailable
		}
		if strings.HasPrefix(code, "invalid_") {
			return EmailErrorValidation
		}
		return EmailErrorApplication
	case http.StatusBadRequest:
		return EmailErrorValidation
	case http.StatusForbidden:
		return EmailErrorInvalidPermission
	case http.StatusNotFound:
		return EmailErrorNotFound
	case http.StatusConflict:
		if code == "template_outdated" {
			return EmailErrorValidation
		}
		return EmailErrorConcurrentIdempotentRequests
	case http.StatusUnprocessableEntity:
		if code == "idempotency_key_reused" {
			return EmailErrorInvalidIdempotentRequest
		}
		return EmailErrorValidation
	case http.StatusTooManyRequests:
		if code == "quota_limited" {
			return EmailErrorMonthlyQuotaExceeded
		}
		return EmailErrorRateLimitExceeded
	case http.StatusServiceUnavailable:
		return EmailErrorServiceUnavailable
	default:
		return EmailErrorApplication
	}
}

// isRetryable says whether waiting can fix the error. A send carries an
// idempotency key, so a retry can't queue a second email.
func (e *EmailError) isRetryable() bool {
	switch e.StatusCode {
	case 0:
		return e.Code == "network_error"
	case http.StatusConflict:
		return e.Code == "idempotency_key_in_flight"
	case http.StatusTooManyRequests:
		return e.Code != "quota_limited"
	default:
		return e.StatusCode == http.StatusServiceUnavailable
	}
}
