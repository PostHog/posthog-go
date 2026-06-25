package posthog

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
)

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
// PostHog-Attempt increments. NOT wired into the live path yet (see PR3).
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
			c.notifyFailure(pendingMsgs, err)
			return
		}

		res, err := c.uploadV1(c.ctx, body, requestId, attempt)
		if err != nil {
			// A 2xx with an unparseable body is terminal for this batch -
			// retrying a malformed success would loop forever.
			if res != nil && isSuccessStatus(res.statusCode) {
				c.notifyFailure(pendingMsgs, err)
				return
			}
			// Transport error: retry unless shutting down or exhausted.
			if c.ctx.Err() != nil {
				c.Errorf("%d messages dropped: shutdown timeout", len(pendingMsgs))
				c.notifyFailure(pendingMsgs, err)
				return
			}
			if lastAttempt {
				c.Errorf("%d messages dropped after %d attempts", len(pendingMsgs), c.maxAttempts)
				c.notifyFailure(pendingMsgs, err)
				return
			}
			if !c.backoffV1(i, res) {
				c.notifyFailure(pendingMsgs, err)
				return
			}
			continue
		}

		if isSuccessStatus(res.statusCode) {
			nextData, nextMsgs, nextUuids := c.partitionV1Results(res, pendingData, pendingMsgs, pendingUuids)
			if len(nextUuids) == 0 {
				return
			}
			if lastAttempt {
				err := fmt.Errorf("capture v1: %d events not persisted after %d attempts", len(nextMsgs), c.maxAttempts)
				c.Errorf("%d messages dropped after %d attempts", len(nextMsgs), c.maxAttempts)
				c.notifyFailure(nextMsgs, err)
				return
			}
			pendingData, pendingMsgs, pendingUuids = nextData, nextMsgs, nextUuids
			if !c.backoffV1(i, res) {
				c.notifyFailure(pendingMsgs, fmt.Errorf("capture v1: shutdown during retry backoff"))
				return
			}
			continue
		}

		if isRetryableStatusV1(res.statusCode) {
			err := requestErrorV1(res)
			if lastAttempt {
				c.Errorf("%d messages dropped after %d attempts", len(pendingMsgs), c.maxAttempts)
				c.notifyFailure(pendingMsgs, err)
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

// uploadV1 performs a single capture-v1 POST. It reuses c.http (which already
// carries BatchUploadTimeout) and the caller's ctx; it does not add a separate
// per-request timeout.
func (c *client) uploadV1(ctx context.Context, b []byte, requestId string, attempt int) (*v1Result, error) {
	body := b
	if c.Compression == CompressionGzip {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(b); err != nil {
			c.Errorf("gzip compression - %s", err)
			return nil, fmt.Errorf("gzip compression: %w", err)
		}
		if err := gw.Close(); err != nil {
			c.Errorf("gzip close - %s", err)
			return nil, fmt.Errorf("gzip close: %w", err)
		}
		body = buf.Bytes()
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
	if c.Compression == CompressionGzip {
		req.Header.Set("Content-Encoding", "gzip")
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

// eventErrorV1 builds the per-event failure error for a "drop" result (or an
// exhausted "retry"). PR5 replaces this with the typed CaptureEventError.
func eventErrorV1(eventUuid string, r eventResult) error {
	if r.Details != nil && *r.Details != "" {
		return fmt.Errorf("capture event %s: %s (%s)", eventUuid, r.Result, *r.Details)
	}
	return fmt.Errorf("capture event %s: %s", eventUuid, r.Result)
}

// requestErrorV1 builds the batch-level failure error for a non-2xx response.
// PR5 replaces this with the typed CaptureRequestError.
func requestErrorV1(res *v1Result) error {
	if res.errResp != nil {
		return fmt.Errorf("capture request failed: %d %s: %s", res.statusCode, res.errResp.Error, res.errResp.ErrorDescription)
	}
	return fmt.Errorf("capture request failed: %d", res.statusCode)
}
