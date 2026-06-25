package posthog

import "fmt"

// CaptureEventError is delivered to Callback.Failure for a single event that the
// capture-v1 endpoint rejected: either a terminal "drop" result, or an event
// still asking to "retry" once the attempt budget is exhausted. Callers can use
// errors.As to inspect the per-event outcome.
//
// This error type is produced exclusively on the 200-with-partial-results path —
// when the batch-level HTTP exchange succeeds but individual events within the
// batch are rejected or not persisted. It is never returned for batch-level
// transport failures (use CaptureRequestError for those via errors.As).
type CaptureEventError struct {
	// EventUUID is the uuid of the rejected event (matches Capture.Uuid etc.).
	EventUUID string
	// Result is the server's per-event directive, e.g. "drop" or "retry".
	Result string
	// Details is the server-supplied explanation, when present (may be empty).
	Details string
	// Exhausted is true when the event was still retryable but the SDK ran out
	// of attempts, false for a server-directed terminal drop.
	Exhausted bool
}

func (e *CaptureEventError) Error() string {
	if e.Exhausted {
		if e.Details != "" {
			return fmt.Sprintf("capture event %s not persisted after retries: %s (%s)", e.EventUUID, e.Result, e.Details)
		}
		return fmt.Sprintf("capture event %s not persisted after retries: %s", e.EventUUID, e.Result)
	}
	if e.Details != "" {
		return fmt.Sprintf("capture event %s: %s (%s)", e.EventUUID, e.Result, e.Details)
	}
	return fmt.Sprintf("capture event %s: %s", e.EventUUID, e.Result)
}

// CaptureRequestError is delivered to Callback.Failure when an entire capture-v1
// request fails: a non-2xx status, a transport error, or a malformed 2xx body.
// It carries the HTTP status and any structured error body the endpoint returned,
// and unwraps to the underlying transport/parse error when there is one.
//
// This error type covers batch-level failures — transport errors, terminal HTTP
// statuses (400/401/429/...), retryable statuses (5xx) whose retry budget is
// exhausted, and 2xx responses with unparseable bodies. For per-event failures
// within a successful batch response, see CaptureEventError.
type CaptureRequestError struct {
	// StatusCode is the HTTP status, or 0 if the request never got a response.
	StatusCode int
	// Code is the machine-readable error from the response body (may be empty).
	Code string
	// Description is the human-readable error from the response body (may be empty).
	Description string
	// Err is the underlying transport or body-parse error, if any.
	Err error
}

func (e *CaptureRequestError) Error() string {
	switch {
	case e.Code != "" || e.Description != "":
		return fmt.Sprintf("capture request failed: %d %s: %s", e.StatusCode, e.Code, e.Description)
	case e.Err != nil && e.StatusCode != 0:
		return fmt.Sprintf("capture request failed: %d: %s", e.StatusCode, e.Err)
	case e.Err != nil:
		return fmt.Sprintf("capture request failed: %s", e.Err)
	default:
		return fmt.Sprintf("capture request failed: %d", e.StatusCode)
	}
}

func (e *CaptureRequestError) Unwrap() error { return e.Err }
