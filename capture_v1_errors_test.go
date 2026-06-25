package posthog

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestV1DropYieldsCaptureEventError(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		details := "billing_limit_exceeded"
		m := map[string]eventResult{uuids[0]: {Result: resultDrop, Details: &details}}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Fatalf("callbacks success=%d failure=%d, want 0/1", s, f)
	}
	var ee *CaptureEventError
	if !errors.As(cb.failures[0].err, &ee) {
		t.Fatalf("failure err = %T (%v), want *CaptureEventError", cb.failures[0].err, cb.failures[0].err)
	}
	if ee.EventUUID != uuidA || ee.Result != resultDrop || ee.Details != "billing_limit_exceeded" || ee.Exhausted {
		t.Errorf("CaptureEventError = %+v", ee)
	}
}

func TestV1ExhaustedRetryYieldsExhaustedEventError(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{uuids[0]: {Result: resultRetry}}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	// maxRetries=0 => a single attempt, so the retry directive is never satisfied.
	c := newV1TestClient(t, ts.URL, cb, 0, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Fatalf("callbacks success=%d failure=%d, want 0/1", s, f)
	}
	var ee *CaptureEventError
	if !errors.As(cb.failures[0].err, &ee) {
		t.Fatalf("failure err = %T, want *CaptureEventError", cb.failures[0].err)
	}
	if !ee.Exhausted || ee.Result != resultRetry || ee.EventUUID != uuidA {
		t.Errorf("CaptureEventError = %+v, want exhausted retry for %s", ee, uuidA)
	}
}

func TestV1TerminalStatusYieldsRequestError(t *testing.T) {
	cb := &recordingCallback{}
	srv := &v1TestServer{respond: func(_ int, _ []string) (int, string, string) {
		return http.StatusBadRequest, `{"error":"invalid_payload","error_description":"bad batch"}`, ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Fatalf("callbacks success=%d failure=%d, want 0/1", s, f)
	}
	var re *CaptureRequestError
	if !errors.As(cb.failures[0].err, &re) {
		t.Fatalf("failure err = %T, want *CaptureRequestError", cb.failures[0].err)
	}
	if re.StatusCode != http.StatusBadRequest || re.Code != "invalid_payload" || re.Description != "bad batch" {
		t.Errorf("CaptureRequestError = %+v", re)
	}
}

func TestV1TransportErrorUnwraps(t *testing.T) {
	cb := &recordingCallback{}
	// Port 1 is unroutable; the POST fails at the transport layer.
	c := newV1TestClient(t, "http://127.0.0.1:1", cb, 0, nil)
	c.sendV1(v1Batch(t, cap1(uuidA)))

	if s, f := cb.counts(); s != 0 || f != 1 {
		t.Fatalf("callbacks success=%d failure=%d, want 0/1", s, f)
	}
	var re *CaptureRequestError
	if !errors.As(cb.failures[0].err, &re) {
		t.Fatalf("failure err = %T, want *CaptureRequestError", cb.failures[0].err)
	}
	if re.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 for transport error", re.StatusCode)
	}
	if errors.Unwrap(re) == nil {
		t.Error("CaptureRequestError.Unwrap() = nil, want the underlying transport error")
	}
}

// capturingLogger records Debugf format strings for assertions.
type capturingLogger struct {
	mu     sync.Mutex
	debugf []string
}

func (l *capturingLogger) Debugf(f string, a ...interface{}) {
	l.mu.Lock()
	l.debugf = append(l.debugf, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *capturingLogger) Logf(string, ...interface{})   {}
func (l *capturingLogger) Warnf(string, ...interface{})  {}
func (l *capturingLogger) Errorf(string, ...interface{}) {}

func (l *capturingLogger) debugContains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.debugf {
		if strings.Contains(f, sub) {
			return true
		}
	}
	return false
}

func TestV1ResultSummaryLogged(t *testing.T) {
	cb := &recordingCallback{}
	log := &capturingLogger{}
	srv := &v1TestServer{respond: func(_ int, uuids []string) (int, string, string) {
		m := map[string]eventResult{}
		for _, u := range uuids {
			m[u] = eventResult{Result: resultOk}
		}
		return http.StatusOK, resultsBody(t, m), ""
	}}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	c := newV1TestClient(t, ts.URL, cb, 9, func(cfg *Config) { cfg.Logger = log })
	c.sendV1(v1Batch(t, cap1(uuidA), cap1(uuidB)))

	if !log.debugContains("capture v1 response request_id=") {
		t.Errorf("expected a per-response debug summary, got debugf=%v", log.debugf)
	}
}
