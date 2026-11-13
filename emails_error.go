package posthog

import (
	"errors"
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
	// enabled for the project. On Attachments it can also mean this PostHog
	// can't take attachments yet; the client can't tell which.
	EmailErrorNotFound EmailErrorName = "not_found"
	// EmailErrorConcurrentIdempotentRequests: a send with the same idempotency
	// key is still in flight.
	EmailErrorConcurrentIdempotentRequests EmailErrorName = "concurrent_idempotent_requests"
	// EmailErrorInvalidIdempotentRequest: the idempotency key was used with
	// another request.
	EmailErrorInvalidIdempotentRequest EmailErrorName = "invalid_idempotent_request"
	// EmailErrorRateLimitExceeded: PostHog, or file storage for an upload,
	// rate limited the request.
	EmailErrorRateLimitExceeded EmailErrorName = "rate_limit_exceeded"
	// EmailErrorMonthlyQuotaExceeded: the project is over its email quota.
	EmailErrorMonthlyQuotaExceeded EmailErrorName = "monthly_quota_exceeded"
	// EmailErrorDailyQuotaExceeded: the project uploaded as many attachment
	// bytes as it can today, counted in UTC. Files PostHog already has don't
	// count, and emails without new attachments still go out.
	EmailErrorDailyQuotaExceeded EmailErrorName = "daily_quota_exceeded"
	// EmailErrorServiceUnavailable: PostHog, file storage for an upload, or
	// the network was unavailable.
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
	// StatusCode is the HTTP status when PostHog, or file storage for an
	// upload, answered, and 0 otherwise.
	// A network_error keeps the status of an answer that broke off mid-body.
	StatusCode int
	Why        string
	Fix        string
	DocsURL    string
	// Field names the request field the error is about, when there is one.
	Field string
	// RetryAfter is how long PostHog, or file storage for an upload, asked the
	// client to wait before it tries again, and 0 when it did not ask.
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

// interruptedBy adds why the wait before a retry ended, such as
// context.DeadlineExceeded, to the last attempt's error.
func (e *EmailError) interruptedBy(cause error) *EmailError {
	e.cause = errors.Join(e.cause, cause)
	return e
}

func newEmailError(code, message string, cause error) *EmailError {
	return &EmailError{Name: emailErrorName(0, code), Code: code, Message: message, cause: cause}
}

// invalidEmail is an argument the client refuses before any request.
func invalidEmail(code, field, message string) *EmailError {
	err := newEmailError(code, message, nil)
	err.Name = EmailErrorValidation
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
		if code == "upload_quota_exceeded" {
			return EmailErrorDailyQuotaExceeded
		}
		return EmailErrorRateLimitExceeded
	case http.StatusServiceUnavailable:
		return EmailErrorServiceUnavailable
	default:
		return EmailErrorApplication
	}
}

// isRetryable says whether waiting can fix the error. A send carries an
// idempotency key, so a retry can't queue a second email. A network_error is
// retryable whatever its status: an answer cut off mid-body can't say what
// PostHog decided, and the retry's full answer does.
func (e *EmailError) isRetryable() bool {
	if e.Code == "network_error" {
		return true
	}
	if e.Code == "attachment_upload_failed" {
		// File storage throttled, timed out or was down; anything else it
		// refused fails the same way again.
		return e.Name == EmailErrorRateLimitExceeded || e.Name == EmailErrorServiceUnavailable
	}
	switch e.StatusCode {
	case 0:
		return false
	case http.StatusConflict:
		return e.Code == "idempotency_key_in_flight"
	case http.StatusTooManyRequests:
		return e.Code != "quota_limited" && e.Code != "upload_quota_exceeded"
	default:
		return e.StatusCode == http.StatusServiceUnavailable
	}
}
