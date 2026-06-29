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

// v1Result is the parsed outcome of a single capture-v1 HTTP attempt.
type v1Result struct {
	statusCode    int
	results       map[string]eventResult
	retryAfter    time.Duration
	hasRetryAfter bool
	// errResp holds the best-effort parsed error body for a non-2xx response.
	errResp *v1ErrorResponse
}

// isRetryableStatusV1 reports whether a non-2xx capture-v1 status should be
// retried. Narrower than the legacy set: 429 is terminal in v1 (matches
// posthog-rs retry.rs).
func isRetryableStatusV1(code int) bool {
	switch code {
	case 408, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// sendV1 delivers a prepared batch to the capture-v1 endpoint with partial
// retry: only events tagged "retry" in the response are re-sent on subsequent
// attempts. PostHog-Request-Id and created_at are stable across attempts;
// PostHog-Attempt increments.
func (c *client) sendV1(pb preparedBatch) {
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
			c.Errorf("marshalling v1 batch wrapper - %s", err)
			c.notifyFailure(pendingMsgs, &CaptureRequestError{Err: err})
			return
		}

		res, err := c.uploadV1(c.ctx, body, requestId, attempt)
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
			if res != nil && res.statusCode != 0 && !isRetryableStatusV1(res.statusCode) {
				c.notifyFailure(pendingMsgs, requestErrorV1(res))
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
			if !c.backoffV1(i, res) {
				c.notifyFailure(pendingMsgs, reqErr)
				return
			}
			continue
		}

		if isSuccessStatus(res.statusCode) {
			c.logV1ResultSummary(requestId, attempt, res.results)
			nextData, nextMsgs, nextUuids := c.partitionV1Results(res, pendingData, pendingMsgs, pendingUuids)
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
			if !c.backoffV1(i, res) {
				c.notifyFailure(pendingMsgs, &CaptureRequestError{Err: errShutdownDuringBackoff})
				return
			}
			continue
		}

		if isRetryableStatusV1(res.statusCode) {
			err := requestErrorV1(res)
			if lastAttempt {
				c.dropMessages(pendingMsgs, err)
				return
			}
			if !c.backoffV1(i, res) {
				c.notifyFailure(pendingMsgs, err)
				return
			}
			continue
		}

		// Terminal non-2xx (400/401/402/413/415/429/...): no retry.
		c.notifyFailure(pendingMsgs, requestErrorV1(res))
		return
	}
}

// partitionV1Results splits a 2xx batch by per-event result: terminal events
// fire their callback now, "retry" events are returned for the next attempt.
// A uuid absent from the results map is silently dropped (no callback).
func (c *client) partitionV1Results(res *v1Result, data []json.RawMessage, msgs []APIMessage, uuids []string) ([]json.RawMessage, []APIMessage, []string) {
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
			c.notifyFailure([]APIMessage{msgs[idx]}, eventErrorV1(id, r))
		default:
			// ok, warning, and any unrecognized result are terminal success.
			c.notifySuccess([]APIMessage{msgs[idx]})
		}
	}
	return nextData, nextMsgs, nextUuids
}

// backoffV1 waits before the next attempt, honoring Retry-After when it exceeds
// the configured backoff. Returns false if shutdown was requested during the wait.
func (c *client) backoffV1(attemptIndex int, res *v1Result) bool {
	retryDelay := c.RetryAfter(attemptIndex)
	if res != nil && res.hasRetryAfter && res.retryAfter > retryDelay {
		retryDelay = res.retryAfter
	}
	retryTimer := time.NewTimer(retryDelay)
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

// compressV1Body compresses the v1 request body per the configured mode and
// returns the body plus the Content-Encoding wire token ("" when uncompressed,
// "br" for brotli). If a configured codec fails, the raw body is returned
// without a Content-Encoding token plus the compression error so callers can
// log it while still sending the batch uncompressed. zstd reuses the shared
// concurrency-safe encoder. The codecs other than gzip are gated to
// CaptureModeAnalyticsV1 by Config.Validate, so this is only reached for modes
// the backend can decode via Content-Encoding.
func compressV1Body(mode CompressionMode, raw []byte) ([]byte, string, error) {
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

// uploadV1 performs a single capture-v1 POST. It reuses c.http (which already
// carries BatchUploadTimeout) and the caller's ctx; it does not add a separate
// per-request timeout.
func (c *client) uploadV1(ctx context.Context, b []byte, requestId string, attempt int) (*v1Result, error) {
	body, encoding, err := compressV1Body(c.Compression, b)
	if err != nil {
		if body == nil {
			c.Errorf("compressing v1 body (%s) - %s", c.Compression, err)
			return nil, err
		}
		c.Warnf("compressing v1 body (%s) failed; sending uncompressed - %s", c.Compression, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+captureV1Path, bytes.NewReader(body))
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
	return c.reportV1(res)
}

// reportV1 reads and classifies a capture-v1 response. For a 2xx it parses the
// per-uuid results (returning an error if the body is unreadable/malformed). For
// a non-2xx it best-effort parses the error body; classification is left to sendV1.
func (c *client) reportV1(res *http.Response) (*v1Result, error) {
	retryAfter, hasRetryAfter := parseRetryAfter(res.Header.Get("Retry-After"), c.now())
	result := &v1Result{statusCode: res.StatusCode, retryAfter: retryAfter, hasRetryAfter: hasRetryAfter}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		c.Errorf("reading v1 response %d %s - %s", res.StatusCode, res.Status, err)
		return result, err
	}

	if isSuccessStatus(res.StatusCode) {
		c.debugf("response %s", res.Status)
		var parsed captureV1Response
		if err := json.Unmarshal(body, &parsed); err != nil {
			c.Errorf("parsing v1 response body %d - %s", res.StatusCode, err)
			return result, fmt.Errorf("parsing v1 response: %w", err)
		}
		result.results = parsed.Results
		return result, nil
	}

	c.Logger.Logf("response %d %s – %s", res.StatusCode, res.Status, string(body))
	var errResp v1ErrorResponse
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

// eventErrorV1 builds the per-event CaptureEventError for a terminal "drop".
func eventErrorV1(eventUuid string, r eventResult) error {
	details := ""
	if r.Details != nil {
		details = *r.Details
	}
	return &CaptureEventError{EventUUID: eventUuid, Result: r.Result, Details: details}
}

// requestErrorV1 builds the batch-level CaptureRequestError for a non-2xx response.
func requestErrorV1(res *v1Result) error {
	return newCaptureRequestError(res, nil)
}

// newCaptureRequestError assembles a *CaptureRequestError from an optional parsed
// result (status + structured error body) and an optional underlying error.
func newCaptureRequestError(res *v1Result, underlying error) *CaptureRequestError {
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

// logV1ResultSummary emits a single verbose debug line per 2xx response that
// tallies per-event directives (ok/warning/drop/retry/other), so operators can
// debug partial-submission outcomes without diffing the raw response body.
func (c *client) logV1ResultSummary(requestId string, attempt int, results map[string]eventResult) {
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
		"capture v1 response request_id=%s attempt=%d events=%d ok=%d warning=%d drop=%d retry=%d other=%d",
		requestId, attempt, len(results), ok, warning, drop, retry, other,
	)
}
