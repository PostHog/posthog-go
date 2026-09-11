package posthog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

// getZstdEncoder returns a shared zstd encoder, lazily initialized on first
// call. klauspost's EncodeAll is safe for concurrent use and the library
// recommends reusing one encoder over allocating per call. The init is
// practically infallible with static options, but returning an error keeps
// SDK setup graceful (no panic on load).
var getZstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
})

// attemptResult is the parsed outcome of a single capture HTTP attempt.
type attemptResult struct {
	statusCode    int
	results       map[string]eventResult
	retryAfter    time.Duration
	hasRetryAfter bool
	// errResp holds the best-effort parsed error body for a non-2xx response.
	errResp *captureErrorResponse
}

// isRetryableStatus reports whether a non-2xx capture status should be
// retried. 429 is terminal: the capture endpoint never emits it (billing is a
// terminal 402), matching posthog-rs retry.rs.
func isRetryableStatus(code int) bool {
	switch code {
	case 408, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// send delivers a prepared batch to the capture endpoint with partial
// retry: only events tagged "retry" in the response are re-sent on subsequent
// attempts. PostHog-Request-Id and created_at are stable across attempts;
// PostHog-Attempt increments.
func (c *client) send(pb preparedBatch) {
	requestId := uuid.New().String()
	createdAt := c.now().UTC().Format(time.RFC3339)

	pendingData := pb.data
	pendingMsgs := pb.msgs
	pendingUuids := pb.uuids

	for i := 0; i < c.maxAttempts; i++ {
		attempt := i + 1
		lastAttempt := i == c.maxAttempts-1

		body, err := json.Marshal(eventBatch{
			CreatedAt:           createdAt,
			HistoricalMigration: c.HistoricalMigration,
			Batch:               pendingData,
		})
		if err != nil {
			c.Errorf("marshalling batch wrapper - %s", err)
			c.notifyFailure(pendingMsgs, &CaptureRequestError{Err: err})
			return
		}

		res, err := c.upload(c.ctx, body, requestId, attempt)
		if err != nil {
			reqErr := newCaptureRequestError(res, err)
			// A 2xx with an unparseable body is terminal for this batch -
			// retrying a malformed success would loop forever.
			if res != nil && isSuccessStatus(res.statusCode) {
				c.notifyFailure(pendingMsgs, reqErr)
				return
			}
			// A terminal non-retryable status (400/401/429/...) whose body
			// read errored: fail fast rather than falling through to the
			// transport-retry path.
			if res != nil && res.statusCode != 0 && !isRetryableStatus(res.statusCode) {
				c.notifyFailure(pendingMsgs, requestError(res))
				return
			}
			// Transport error: retry unless shutting down or exhausted.
			if c.ctx.Err() != nil {
				c.Errorf("%d messages dropped: shutdown timeout", len(pendingMsgs))
				c.notifyFailure(pendingMsgs, reqErr)
				return
			}
			if lastAttempt {
				c.dropMessages(pendingMsgs, reqErr)
				return
			}
			if !c.waitBackoff(i, res) {
				c.notifyFailure(pendingMsgs, reqErr)
				return
			}
			continue
		}

		if isSuccessStatus(res.statusCode) {
			c.logResultSummary(requestId, attempt, res.results)
			nextData, nextMsgs, nextUuids := c.partitionResults(res, pendingData, pendingMsgs, pendingUuids)
			if len(nextUuids) == 0 {
				return
			}
			if lastAttempt {
				c.Errorf("%d messages dropped after %d attempts", len(nextMsgs), c.maxAttempts)
				for idx, id := range nextUuids {
					var details string
					if r, ok := res.results[id]; ok && r.Details != nil {
						details = *r.Details
					}
					c.notifyFailure([]APIMessage{nextMsgs[idx]}, &CaptureEventError{EventUUID: id, Result: resultRetry, Details: details, Exhausted: true})
				}
				return
			}
			pendingData, pendingMsgs, pendingUuids = nextData, nextMsgs, nextUuids
			if !c.waitBackoff(i, res) {
				c.notifyFailure(pendingMsgs, &CaptureRequestError{Err: errShutdownDuringBackoff})
				return
			}
			continue
		}

		if isRetryableStatus(res.statusCode) {
			err := requestError(res)
			if lastAttempt {
				c.dropMessages(pendingMsgs, err)
				return
			}
			if !c.waitBackoff(i, res) {
				c.notifyFailure(pendingMsgs, err)
				return
			}
			continue
		}

		// Terminal non-2xx (400/401/402/413/415/429/...): no retry.
		c.notifyFailure(pendingMsgs, requestError(res))
		return
	}
}

// partitionResults splits a 2xx batch by per-event result: terminal events
// fire their callback now, "retry" events are returned for the next attempt.
// A uuid absent from the results map is silently dropped (no callback).
func (c *client) partitionResults(res *attemptResult, data []json.RawMessage, msgs []APIMessage, uuids []string) ([]json.RawMessage, []APIMessage, []string) {
	var nextData []json.RawMessage
	var nextMsgs []APIMessage
	var nextUuids []string
	for idx, id := range uuids {
		r, ok := res.results[id]
		if !ok {
			// Matches posthog-rs: events absent from the results map are treated
			// as accepted (no retry, no error callback).
			continue
		}
		switch r.Result {
		case resultRetry:
			nextData = append(nextData, data[idx])
			nextMsgs = append(nextMsgs, msgs[idx])
			nextUuids = append(nextUuids, id)
		case resultDrop:
			c.notifyFailure([]APIMessage{msgs[idx]}, eventError(id, r))
		default:
			// ok, warning, and any unrecognized result are terminal success.
			c.notifySuccess([]APIMessage{msgs[idx]})
		}
	}
	return nextData, nextMsgs, nextUuids
}

// retryDelay is the pure delay decision for the next attempt: the configured
// backoff, raised to the server Retry-After when it is larger (Retry-After is a
// minimum, not a replacement). The Retry-After is clamped to defaultMaxBackoff
// so a hostile or buggy header can't park a batch goroutine; the configured
// backoff itself (Config.RetryAfter) is never truncated.
func (c *client) retryDelay(attemptIndex int, res *attemptResult) time.Duration {
	retryDelay := c.RetryAfter(attemptIndex)
	if res != nil && res.hasRetryAfter {
		clamped := res.retryAfter
		if clamped > defaultMaxBackoff {
			clamped = defaultMaxBackoff
		}
		if clamped > retryDelay {
			retryDelay = clamped
		}
	}
	return retryDelay
}

// waitBackoff waits before the next attempt using retryDelay. Returns false if
// shutdown was requested during the wait.
func (c *client) waitBackoff(attemptIndex int, res *attemptResult) bool {
	retryTimer := time.NewTimer(c.retryDelay(attemptIndex, res))
	select {
	case <-retryTimer.C:
		return true
	case <-c.quit:
		if !retryTimer.Stop() {
			<-retryTimer.C
		}
		return false
	case <-c.ctx.Done():
		if !retryTimer.Stop() {
			<-retryTimer.C
		}
		return false
	}
}

// dropMessages logs and notifies failure for events that are abandoned after
// exhausting all retry attempts.
func (c *client) dropMessages(msgs []APIMessage, err error) {
	c.Errorf("%d messages dropped after %d attempts", len(msgs), c.maxAttempts)
	c.notifyFailure(msgs, err)
}

// compressBody compresses the request body per the configured mode and
// returns the body plus the Content-Encoding wire token ("" when uncompressed,
// "br" for brotli). If a configured codec fails, the raw body is returned
// without a Content-Encoding token plus the compression error so callers can
// log it while still sending the batch uncompressed. zstd reuses the shared
// concurrency-safe encoder. Config.Validate rejects unknown modes, so only
// codecs the backend can decode via Content-Encoding reach here.
func compressBody(mode CompressionMode, raw []byte) ([]byte, string, error) {
	switch mode {
	case CompressionNone:
		return raw, "", nil
	case CompressionGzip:
		body, err := compressGzip(raw)
		if err != nil {
			return raw, "", err
		}
		return body, "gzip", nil
	case CompressionDeflate:
		// zlib (RFC 1950) wraps the deflate stream so it begins with 0x78; the
		// backend sniffs that byte to route Content-Encoding: deflate to its
		// zlib decoder, avoiding ambiguity with raw (headerless) deflate.
		body, err := deflateCompress(raw)
		if err != nil {
			return raw, "", err
		}
		return body, "deflate", nil
	case CompressionZstd:
		enc, err := getZstdEncoder()
		if err != nil {
			return raw, "", fmt.Errorf("zstd encoder init: %w", err)
		}
		return enc.EncodeAll(raw, make([]byte, 0, len(raw))), "zstd", nil
	case CompressionBrotli:
		body, err := brotliCompress(raw)
		if err != nil {
			return raw, "", err
		}
		return body, "br", nil
	default:
		return nil, "", fmt.Errorf("unsupported compression mode %s", mode)
	}
}

// upload performs a single capture POST. It reuses c.http (which already
// carries BatchUploadTimeout) and the caller's ctx; it does not add a separate
// per-request timeout.
func (c *client) upload(ctx context.Context, b []byte, requestId string, attempt int) (*attemptResult, error) {
	body, encoding, err := compressBody(c.Compression, b)
	if err != nil {
		if body == nil {
			c.Errorf("compressing body (%s) - %s", c.Compression, err)
			return nil, err
		}
		c.Warnf("compressing body (%s) failed; sending uncompressed - %s", c.Compression, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+capturePath, bytes.NewReader(body))
	if err != nil {
		c.Errorf("creating request - %s", err)
		return nil, err
	}

	sdkInfo := SDKName + "/" + getVersion()
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", sdkInfo)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PostHog-Sdk-Info", sdkInfo)
	req.Header.Set("PostHog-Attempt", strconv.Itoa(attempt))
	req.Header.Set("PostHog-Request-Id", requestId)
	req.Header.Set("PostHog-Request-Timestamp", c.now().UTC().Format(time.RFC3339))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}

	res, err := c.http.Do(req)
	if err != nil {
		c.Warnf("sending request - %s", err)
		return nil, err
	}
	defer res.Body.Close()
	return c.report(res)
}

// report reads and classifies a capture response. For a 2xx it parses the
// per-uuid results (returning an error if the body is unreadable/malformed). For
// a non-2xx it best-effort parses the error body; classification is left to send.
func (c *client) report(res *http.Response) (*attemptResult, error) {
	retryAfter, hasRetryAfter := parseRetryAfter(res.Header.Get("Retry-After"), c.now())
	result := &attemptResult{statusCode: res.StatusCode, retryAfter: retryAfter, hasRetryAfter: hasRetryAfter}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		c.Errorf("reading capture response %d %s - %s", res.StatusCode, res.Status, err)
		return result, err
	}

	if isSuccessStatus(res.StatusCode) {
		c.debugf("response %s", res.Status)
		var parsed captureResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			c.Errorf("parsing capture response body %d - %s", res.StatusCode, err)
			return result, fmt.Errorf("parsing capture response: %w", err)
		}
		result.results = parsed.Results
		return result, nil
	}

	c.Logger.Logf("response %d %s – %s", res.StatusCode, res.Status, string(body))
	var errResp captureErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && (errResp.Error != "" || errResp.ErrorDescription != "") {
		result.errResp = &errResp
	}
	return result, nil
}

func isSuccessStatus(code int) bool {
	return code >= 200 && code <= 299
}

// errShutdownDuringBackoff is the underlying cause when retries are abandoned
// because the client is shutting down mid-backoff.
var errShutdownDuringBackoff = errors.New("shutdown during retry backoff")

// eventError builds the per-event CaptureEventError for a terminal "drop".
func eventError(eventUuid string, r eventResult) error {
	details := ""
	if r.Details != nil {
		details = *r.Details
	}
	return &CaptureEventError{EventUUID: eventUuid, Result: r.Result, Details: details}
}

// requestError builds the batch-level CaptureRequestError for a non-2xx response.
func requestError(res *attemptResult) error {
	return newCaptureRequestError(res, nil)
}

// newCaptureRequestError assembles a *CaptureRequestError from an optional parsed
// result (status + structured error body) and an optional underlying error.
func newCaptureRequestError(res *attemptResult, underlying error) *CaptureRequestError {
	e := &CaptureRequestError{Err: underlying}
	if res != nil {
		e.StatusCode = res.statusCode
		if res.errResp != nil {
			e.Code = res.errResp.Error
			e.Description = res.errResp.ErrorDescription
		}
	}
	return e
}

// logResultSummary emits a single verbose debug line per 2xx response that
// tallies per-event directives (ok/warning/drop/retry/other), so operators can
// debug partial-submission outcomes without diffing the raw response body.
func (c *client) logResultSummary(requestId string, attempt int, results map[string]eventResult) {
	var ok, warning, drop, retry, other int
	for _, r := range results {
		switch r.Result {
		case resultOk:
			ok++
		case resultWarning:
			warning++
		case resultDrop:
			drop++
		case resultRetry:
			retry++
		default:
			other++
		}
	}
	c.debugf(
		"capture response request_id=%s attempt=%d events=%d ok=%d warning=%d drop=%d retry=%d other=%d",
		requestId, attempt, len(results), ok, warning, drop, retry, other,
	)
}
