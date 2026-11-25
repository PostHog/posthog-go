// Package evaluations manages AI datasets, scorer definitions, and offline
// experiments through the acknowledged PostHog project API.
//
// Set Config.SecretKey to a project secret API key for the configured project or a
// personal API key. Dataset and scorer management use dataset and llm_analytics
// scopes respectively. A key with
// offline_evaluation_ingestion:write can create runs, submit outcomes and close
// runs using previously saved dataset and scorer version IDs. All network calls
// take a context and return after acknowledgment. Use errors.As to inspect an
// [APIError] or recover the unfinished portion of a [BulkError].
//
// The client reads up to 128 MiB for a successful list page and 4 MiB for
// other responses. If a page exceeds the limit, request a smaller page.
package evaluations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config configures one project. Timeout defaults to 15 seconds per attempt.
// MaxRetries defaults to three additional attempts. Set it to a pointer to zero
// to disable retries. Host may include a reverse-proxy path prefix. Use HTTPS
// for remote hosts. An explicit HTTP host sends the secret key in cleartext.
type Config struct {
	ProjectID  int
	SecretKey  string
	Host       string
	Timeout    time.Duration
	MaxRetries *int
	Transport  http.RoundTripper
}

type Client struct {
	Datasets      Datasets
	Scorers       Scorers
	project       int
	base, secret  string
	http          *http.Client
	retries       int
	closed        context.Context
	close         context.CancelFunc
	ownsTransport bool
}

// NewClient configures project-scoped dataset, scorer, and experiment operations.
// It validates the configuration locally. Call Close to release its resources.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ProjectID <= 0 || cfg.SecretKey == "" || !validText(cfg.SecretKey) || strings.IndexFunc(cfg.SecretKey, unicode.IsSpace) >= 0 {
		return nil, errors.New("evaluations: positive project ID and a secret key without whitespace required")
	}
	if cfg.Host == "" {
		cfg.Host = "https://us.posthog.com"
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(cfg.Host, "?#") {
		return nil, errors.New("evaluations: invalid app API host")
	}
	if u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") {
		switch strings.ToLower(u.Hostname()) {
		case "app.posthog.com", "us.i.posthog.com":
			u.Host = "us.posthog.com"
		case "eu.i.posthog.com":
			u.Host = "eu.posthog.com"
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	retries := 3
	if cfg.MaxRetries != nil {
		retries = *cfg.MaxRetries
	}
	if cfg.Timeout < 0 || retries < 0 {
		return nil, errors.New("evaluations: invalid timeout or retry count")
	}
	transport := cfg.Transport
	owns := false
	if transport == nil {
		transport = http.DefaultTransport
		if t, ok := transport.(*http.Transport); ok {
			transport = t.Clone()
			owns = true
		}
	}
	closed, cancel := context.WithCancel(context.Background())
	c := &Client{base: strings.TrimRight(u.String(), "/") + "/api/projects/" + strconv.Itoa(cfg.ProjectID) + "/",
		project: cfg.ProjectID, secret: cfg.SecretKey, retries: retries, closed: closed, close: cancel, ownsTransport: owns,
		http: &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	c.Datasets = Datasets{c}
	c.Scorers = Scorers{c}
	return c, nil
}

// Close cancels in-flight requests and releases owned HTTP connections.
func (c *Client) Close() {
	c.close()
	if c.ownsTransport {
		c.http.CloseIdleConnections()
	}
}

type Persistence string

const (
	NotSent  Persistence = "not_sent"
	Rejected Persistence = "rejected"
	Unknown  Persistence = "unknown"
)

// APIError retains server details for explicit inspection. Error omits server
// text, which can include submitted payloads. Submission enables exact creation replay.
type APIError struct {
	Status         int
	Code, Attr     string
	Detail, Errors any
	Response       Fields
	RetryAfter     *time.Duration
	Persistence    Persistence
	Submission     Fields
	cause          error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("evaluations: request failed (HTTP %d, persistence %s)", e.Status, e.Persistence)
}
func (e *APIError) GoString() string { return e.Error() }
func (e *APIError) Unwrap() error    { return e.cause }
func invalidResponse(status int) *APIError {
	return &APIError{Status: status, Code: "invalid_response", Persistence: Unknown}
}
func closedError() *APIError { return &APIError{Code: "client_closed", Persistence: NotSent} }

type request struct {
	method, path string
	query        url.Values
	body         []byte
	retry        bool
}

const (
	maxResponseBytes = 4 << 20
	// A dataset page can contain 100 records with up to 1 MB of metadata each.
	maxPageResponseBytes = 128 << 20
)

var errResponseTooLarge = errors.New("evaluations: response body exceeds size limit")

func readResponseBody(body io.ReadCloser, limit int64) ([]byte, error) {
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(raw)) > limit {
		return nil, errResponseTooLarge
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// call parses each successful response exactly once. Parsing errors never trigger a retry.
func call[T any](ctx context.Context, c *Client, r request, parse func(Fields) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.closed, cancel)
	defer stop()
	endpoint := c.base + r.path
	if len(r.query) > 0 {
		endpoint += "?" + r.query.Encode()
	}
	hadUnknown := false
	var lastFailure *APIError
	for attempt := 0; ; attempt++ {
		err := c.closed.Err()
		if err != nil && lastFailure == nil {
			return zero, closedError()
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			if lastFailure != nil {
				lastFailure.cause = err
				return zero, lastFailure
			}
			return zero, err
		}
		req, err := http.NewRequestWithContext(ctx, r.method, endpoint, bytes.NewReader(r.body))
		if err != nil {
			return zero, err
		}
		req.Header.Set("Authorization", "Bearer "+c.secret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := c.http.Do(req)
		failure := &APIError{Code: "transport_error", Persistence: Unknown, cause: err}
		canRetry := true // Transport failures may be retried for replayable operations.
		if err == nil {
			success := res.StatusCode >= 200 && res.StatusCode < 300
			limit := int64(maxResponseBytes)
			if success && r.method == http.MethodGet && r.query.Has("limit") {
				limit = maxPageResponseBytes
			}
			raw, readErr := readResponseBody(res.Body, limit)
			if success && readErr == nil {
				var data Fields
				if r.method == "DELETE" {
					if res.StatusCode != 204 {
						return zero, invalidResponse(res.StatusCode)
					}
				} else if res.StatusCode == http.StatusNoContent || decode(raw, &data) != nil || data == nil {
					return zero, invalidResponse(res.StatusCode)
				}
				value, err := parse(data)
				if err != nil {
					failure := invalidResponse(res.StatusCode)
					// Parsers normalize IDs and timestamps. Retain the original
					// server fields for callers inspecting a malformed acknowledgment.
					_ = decode(raw, &failure.Response)
					failure.Detail = "The API returned an invalid acknowledgment."
					return zero, failure
				}
				return value, nil
			}
			// Status and retry headers still apply when the body is incomplete.
			failure = httpError(res, raw)
			failure.cause = readErr
			if success {
				if errors.Is(readErr, errResponseTooLarge) {
					failure.Code = "invalid_response"
					failure.Detail = errResponseTooLarge.Error()
					return zero, failure
				}
				failure.Code = "transport_error"
			} else {
				canRetry = retryable(res.StatusCode)
			}
		}
		hadUnknown = hadUnknown || failure.Persistence == Unknown
		if hadUnknown {
			failure.Persistence = Unknown
		}
		if err := ctx.Err(); err != nil {
			failure.cause = err
			return zero, failure
		}
		lastFailure = failure
		if !(r.retry || r.method == "GET") || attempt >= c.retries || !canRetry {
			return zero, failure
		}
		wait := retryDelay(attempt, failure.RetryAfter, rand.Float64())
		if wait > maxRetryWait {
			return zero, failure
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			failure.cause = ctx.Err()
			return zero, failure
		case <-timer.C:
		}
	}
}

const maxRetryWait = 30 * time.Second

func retryDelay(attempt int, retryAfter *time.Duration, random float64) time.Duration {
	base := (500 * time.Millisecond) << min(attempt, 6)
	wait := min(maxRetryWait, time.Duration(float64(base)*(0.8+0.4*random)))
	if retryAfter != nil {
		wait = max(wait, *retryAfter)
	}
	return wait
}
func retryable(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}
func httpError(res *http.Response, raw []byte) *APIError {
	e := &APIError{Status: res.StatusCode, Persistence: Unknown}
	if e.Status >= 400 && e.Status < 500 && e.Status != 408 {
		e.Persistence = Rejected
	}
	var detail any
	if decode(raw, &detail) == nil {
		e.Detail = detail
		if obj, ok := detail.(map[string]any); ok {
			e.Response = obj
			e.Code, _ = obj["code"].(string)
			e.Attr, _ = obj["attr"].(string)
			e.Errors = obj["errors"]
			if obj["detail"] != nil {
				e.Detail = obj["detail"]
			}
		}
	}
	if s := res.Header.Get("Retry-After"); s != "" {
		var wait time.Duration
		if sec, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(sec) {
			if sec >= float64(math.MaxInt64)/float64(time.Second) {
				wait = time.Duration(math.MaxInt64)
			} else {
				wait = time.Duration(max(0, sec) * float64(time.Second))
			}
		} else if at, err := http.ParseTime(s); err == nil {
			wait = max(0, time.Until(at))
		} else {
			return e
		}
		e.RetryAfter = &wait
	}
	return e
}

func decode(data []byte, out any) error {
	if !validJSONText(data) {
		return errors.New("evaluations: invalid Unicode in JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// encoding/json replaces malformed UTF-8 and unpaired UTF-16 surrogates with
// U+FFFD. Reject those inputs before they can change a saved declaration.
func validJSONText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) || data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil || n == 0 || (n >= 0xdc00 && n <= 0xdfff) {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
