package posthog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

const (
	testTemplateID = "0190f6d2-7c1e-7b3a-9f5e-1c2d3e4f5a6b"
	testMessageID  = "01a18356-c56c-0001-e494-7666bc915188"
)

type receivedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]interface{}
}

type stubReply struct {
	status int
	body   string
	header map[string]string
}

// emailServer answers each request with the next reply and records what it received.
type emailServer struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []stubReply
	requests []receivedRequest
}

func newEmailServer(t *testing.T, replies ...stubReply) *emailServer {
	s := &emailServer{replies: replies}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isEmailPath(r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &body))
		}
		s.mu.Lock()
		s.requests = append(s.requests, receivedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		reply := stubReply{status: http.StatusInternalServerError, body: `{"detail":"no reply left"}`}
		if len(s.replies) > 0 {
			reply, s.replies = s.replies[0], s.replies[1:]
		}
		s.mu.Unlock()
		for name, value := range reply.header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *emailServer) received() []receivedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]receivedRequest(nil), s.requests...)
}

func accepted(messageID string) stubReply {
	return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + messageID + `","replayed":false,"warnings":[]}`}
}

func failed(status int, code string) stubReply {
	return stubReply{status: status, body: `{"type":"validation_error","code":"` + code + `","detail":"PostHog said no."}`}
}

type emailClientOption func(*Config)

func newEmailClient(t *testing.T, server *emailServer, options ...emailClientOption) Client {
	config := Config{Endpoint: server.URL, AppHost: server.URL, SecretKey: "phs_test", Logger: testLogger{t.Logf, t.Logf}}
	for _, option := range options {
		option(&config)
	}
	c, err := NewWithConfig("phc_test", config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	senderOf(c).backoff = func(int) time.Duration { return 0 }
	return c
}

func senderOf(c Client) *emailSender { return c.(*client).emails }

func templateEmail() TemplateEmail {
	return TemplateEmail{To: "user@example.com", TemplateID: testTemplateID}
}

func requireEmailError(t *testing.T, err error) *EmailError {
	t.Helper()
	var emailErr *EmailError
	require.True(t, errors.As(err, &emailErr), "want *EmailError, got %v", err)
	return emailErr
}

func TestEmailsSendTemplate(t *testing.T) {
	server := newEmailServer(t, accepted("msg_1"))
	client := newEmailClient(t, server)

	result, err := client.Emails().Send(context.Background(), TemplateEmail{
		To:           "user@example.com",
		TemplateID:   "0190F6D2-7C1E-7B3A-9F5E-1C2D3E4F5A6B",
		TemplateData: map[string]interface{}{"name": "Ada"},
		DistinctID:   "user_123",
	})

	require.NoError(t, err)
	require.Equal(t, &SendEmailResult{ID: "msg_1", Warnings: []EmailWarning{}}, result)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, "/api/projects/@current/messaging/send/email/", requests[0].Path)
	require.Equal(t, map[string]interface{}{
		"template_id":  testTemplateID,
		"to":           "user@example.com",
		"message_data": map[string]interface{}{"name": "Ada"},
		"distinct_id":  "user_123",
	}, requests[0].Body)
}

func TestEmailsSendTemplateWithoutDataOrDistinctID(t *testing.T) {
	server := newEmailServer(t, accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"template_id":  testTemplateID,
		"to":           "user@example.com",
		"message_data": map[string]interface{}{},
	}, server.received()[0].Body)
}

func TestEmailsSendCode(t *testing.T) {
	server := newEmailServer(t, accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), CodeEmail{
		To:          "user@example.com",
		TemplateKey: "password-reset",
		Subject:     "Reset your password",
		HTML:        "<p>Reset & go</p>",
		Text:        "Reset and go",
		From:        "Acme <security@example.com>",
		ReplyTo:     []string{"support@example.com"},
		DistinctID:  "user_123",
	})

	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"to":                 "user@example.com",
		"template_key":       "password-reset",
		"subject":            "Reset your password",
		"html":               "<p>Reset & go</p>",
		"text":               "Reset and go",
		"from":               "Acme <security@example.com>",
		"reply_to":           []interface{}{"support@example.com"},
		"capture_engagement": true,
		"distinct_id":        "user_123",
	}, server.received()[0].Body)
}

func TestEmailsSendCodeLeavesOutUnsetFields(t *testing.T) {
	server := newEmailServer(t, accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), CodeEmail{
		To:                "user@example.com",
		TemplateKey:       "digest",
		Subject:           "Your digest",
		Text:              "Plain text only",
		CaptureEngagement: Ptr(false),
	})

	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"to":                 "user@example.com",
		"template_key":       "digest",
		"subject":            "Your digest",
		"text":               "Plain text only",
		"capture_engagement": false,
	}, server.received()[0].Body)
}

func TestEmailsSendHeaders(t *testing.T) {
	server := newEmailServer(t, accepted("msg_1"))
	client := newEmailClient(t, server, func(c *Config) {
		c.SecretKey = "  phs_test  "
		c.MigratedFrom = "resend"
	})

	_, err := client.Emails().Send(context.Background(), TemplateEmail{
		To: "user@example.com", TemplateID: testTemplateID, IdempotencyKey: "welcome:user_123",
	})

	require.NoError(t, err)
	header := server.received()[0].Header
	require.Equal(t, "Bearer phs_test", header.Get("Authorization"))
	require.Equal(t, "application/json", header.Get("Content-Type"))
	require.Equal(t, "application/json", header.Get("Accept"))
	require.Equal(t, "welcome:user_123", header.Get("Idempotency-Key"))
	require.Equal(t, "posthog-go/"+getVersion(), header.Get("User-Agent"))
	require.Equal(t, "resend", header.Get("X-PostHog-Migrated-From"))
}

func TestEmailsSendResult(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusAccepted, body: `{
		"message_id": "msg_1",
		"replayed": true,
		"warnings": [{"code": "template_superseded", "detail": "A newer version exists.", "fix": "Upload it."}, "junk"]
	}`})
	client := newEmailClient(t, server)

	result, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, &SendEmailResult{
		ID:       "msg_1",
		Replayed: true,
		Warnings: []EmailWarning{{Code: "template_superseded", Message: "A newer version exists.", Fix: "Upload it."}},
	}, result)
}

func TestEmailsSendLogsWarnings(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusAccepted, body: `{"message_id":"msg_1","warnings":[{"code":"template_superseded","detail":"A newer version exists."}]}`})
	logs := &recordingLogger{}
	client := newEmailClient(t, server, func(c *Config) { c.Logger = logs })

	_, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, []string{"PostHog email warning template_superseded: A newer version exists."}, logs.warnings())
}

func TestEmailsAppHost(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		appHost  string
		want     string
	}{
		{name: "default endpoint", want: "https://us.posthog.com"},
		{name: "US ingestion host", endpoint: "https://us.i.posthog.com", want: "https://us.posthog.com"},
		{name: "EU ingestion host", endpoint: "https://eu.i.posthog.com/", want: "https://eu.posthog.com"},
		{name: "EU app host", endpoint: "https://eu.posthog.com", want: "https://eu.posthog.com"},
		{name: "legacy app host", endpoint: "https://app.posthog.com", want: "https://us.posthog.com"},
		{name: "explicit app host wins", endpoint: "https://us.i.posthog.com", appHost: "https://posthog.example.com/", want: "https://posthog.example.com"},
		{name: "explicit cloud ingestion host maps", endpoint: "https://proxy.example.com", appHost: "https://eu.i.posthog.com", want: "https://eu.posthog.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotURL string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotURL = r.URL.String()
				return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"message_id":"msg_1"}`)), Header: http.Header{}}, nil
			})
			client, err := NewWithConfig("phc_test", Config{Endpoint: tt.endpoint, AppHost: tt.appHost, SecretKey: "phs_test", Transport: transport})
			require.NoError(t, err)
			defer client.Close()

			_, err = client.Emails().Send(context.Background(), templateEmail())

			require.NoError(t, err)
			require.Equal(t, tt.want+"/api/projects/@current/messaging/send/email/", gotURL)
		})
	}
}

func TestEmailsSendRefusedBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		code    string
		errName EmailErrorName
	}{
		{name: "custom host without app host", config: Config{Endpoint: "https://proxy.example.com", SecretKey: "phs_test"}, code: "missing_app_host", errName: EmailErrorApplication},
		{name: "no secret key", config: Config{}, code: "missing_secret_key", errName: EmailErrorMissingAPIKey},
		{name: "blank secret key", config: Config{SecretKey: "   "}, code: "missing_secret_key", errName: EmailErrorMissingAPIKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			tt.config.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("no request expected")
			})
			client, err := NewWithConfig("phc_test", tt.config)
			require.NoError(t, err)
			defer client.Close()

			_, err = client.Emails().Send(context.Background(), templateEmail())

			emailErr := requireEmailError(t, err)
			require.Equal(t, tt.code, emailErr.Code)
			require.Equal(t, tt.errName, emailErr.Name)
			require.Zero(t, emailErr.StatusCode)
			require.Zero(t, requests)
		})
	}
}

func TestEmailsSendChecksArguments(t *testing.T) {
	code := func(change func(*CodeEmail)) CodeEmail {
		email := CodeEmail{To: "user@example.com", TemplateKey: "welcome", Subject: "Hi", HTML: "<p>Hi</p>"}
		change(&email)
		return email
	}
	template := func(change func(*TemplateEmail)) TemplateEmail {
		email := templateEmail()
		change(&email)
		return email
	}
	tests := []struct {
		name  string
		email Email
		code  string
		field string
	}{
		{name: "no form", email: nil, code: "invalid_form"},
		{name: "empty recipient", email: template(func(e *TemplateEmail) { e.To = "  " }), code: "invalid_recipient", field: "To"},
		{name: "template ID not a UUID", email: template(func(e *TemplateEmail) { e.TemplateID = "welcome" }), code: "invalid_template_id", field: "TemplateID"},
		{name: "idempotency key with a space", email: template(func(e *TemplateEmail) { e.IdempotencyKey = "welcome user" }), code: "invalid_idempotency_key", field: "IdempotencyKey"},
		{name: "idempotency key too long", email: template(func(e *TemplateEmail) { e.IdempotencyKey = strings.Repeat("k", 256) }), code: "invalid_idempotency_key", field: "IdempotencyKey"},
		{name: "negative retries", email: template(func(e *TemplateEmail) { e.MaxRetries = Ptr(-1) }), code: "invalid_max_retries", field: "MaxRetries"},
		{name: "template data that is not JSON", email: template(func(e *TemplateEmail) { e.TemplateData = map[string]interface{}{"f": func() {}} }), code: "invalid_template_data", field: "TemplateData"},
		{name: "code empty recipient", email: code(func(e *CodeEmail) { e.To = "" }), code: "invalid_recipient", field: "To"},
		{name: "template key with a space", email: code(func(e *CodeEmail) { e.TemplateKey = "pass reset" }), code: "invalid_template_key", field: "TemplateKey"},
		{name: "template key too long", email: code(func(e *CodeEmail) { e.TemplateKey = strings.Repeat("k", 401) }), code: "invalid_template_key", field: "TemplateKey"},
		{name: "no subject", email: code(func(e *CodeEmail) { e.Subject = "" }), code: "invalid_form", field: "Subject"},
		{name: "no html or text", email: code(func(e *CodeEmail) { e.HTML = "" }), code: "invalid_form", field: "HTML"},
		{name: "blank reply-to", email: code(func(e *CodeEmail) { e.ReplyTo = []string{"a@example.com", " "} }), code: "invalid_reply_to", field: "ReplyTo"},
		{name: "code idempotency key", email: code(func(e *CodeEmail) { e.IdempotencyKey = "é" }), code: "invalid_idempotency_key", field: "IdempotencyKey"},
		{name: "code negative retries", email: code(func(e *CodeEmail) { e.MaxRetries = Ptr(-2) }), code: "invalid_max_retries", field: "MaxRetries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t)
			client := newEmailClient(t, server)

			result, err := client.Emails().Send(context.Background(), tt.email)

			require.Nil(t, result)
			emailErr := requireEmailError(t, err)
			require.Equal(t, tt.code, emailErr.Code)
			require.Equal(t, EmailErrorValidation, emailErr.Name)
			require.Equal(t, tt.field, emailErr.Field)
			require.Empty(t, server.received())
		})
	}
}

func TestEmailsSendGeneratesOneIdempotencyKeyForEveryAttempt(t *testing.T) {
	server := newEmailServer(t, failed(http.StatusServiceUnavailable, "delivery_unavailable"), accepted("msg_1"))
	client := newEmailClient(t, server)

	result, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, "msg_1", result.ID)
	requests := server.received()
	require.Len(t, requests, 2)
	key := requests[0].Header.Get("Idempotency-Key")
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), key)
	require.Equal(t, key, requests[1].Header.Get("Idempotency-Key"))
}

func TestEmailsSendRetries(t *testing.T) {
	tests := []struct {
		name     string
		first    stubReply
		requests int
	}{
		{name: "service unavailable", first: failed(http.StatusServiceUnavailable, "delivery_unavailable"), requests: 2},
		{name: "service unavailable without a code", first: stubReply{status: http.StatusServiceUnavailable, body: "<html>Bad gateway</html>"}, requests: 2},
		{name: "throttled", first: failed(http.StatusTooManyRequests, "throttled"), requests: 2},
		{name: "key in flight", first: failed(http.StatusConflict, "idempotency_key_in_flight"), requests: 2},
		{name: "monthly quota", first: failed(http.StatusTooManyRequests, "quota_limited"), requests: 1},
		{name: "outdated template", first: failed(http.StatusConflict, "template_outdated"), requests: 1},
		{name: "suppressed recipient", first: failed(http.StatusUnprocessableEntity, "recipient_suppressed"), requests: 1},
		{name: "bad request", first: failed(http.StatusBadRequest, "sender_not_verified"), requests: 1},
		{name: "server error", first: failed(http.StatusInternalServerError, "internal"), requests: 1},
		{name: "accepted without a message ID", first: stubReply{status: http.StatusAccepted, body: `{}`}, requests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, tt.first, accepted("msg_1"))
			client := newEmailClient(t, server)

			_, _ = client.Emails().Send(context.Background(), templateEmail())

			require.Len(t, server.received(), tt.requests)
		})
	}
}

func TestEmailsSendRetriesNetworkErrors(t *testing.T) {
	attempts := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("connection reset")
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"message_id":"msg_1"}`)), Header: http.Header{}}, nil
	})
	client, err := NewWithConfig("phc_test", Config{SecretKey: "phs_test", Transport: transport})
	require.NoError(t, err)
	defer client.Close()
	senderOf(client).backoff = func(int) time.Duration { return 0 }

	result, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, "msg_1", result.ID)
	require.Equal(t, 2, attempts)
}

func TestEmailsSendRetryLimits(t *testing.T) {
	unavailable := failed(http.StatusServiceUnavailable, "delivery_unavailable")
	tests := []struct {
		name         string
		clientLimit  *int
		sendLimit    *int
		wantRequests int
	}{
		{name: "default", wantRequests: 3},
		{name: "client limit", clientLimit: Ptr(1), wantRequests: 2},
		{name: "client turns retries off", clientLimit: Ptr(0), wantRequests: 1},
		{name: "send overrides client", clientLimit: Ptr(2), sendLimit: Ptr(0), wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, unavailable, unavailable, unavailable, unavailable)
			client := newEmailClient(t, server, func(c *Config) { c.EmailRequestMaxRetries = tt.clientLimit })
			email := templateEmail()
			email.MaxRetries = tt.sendLimit

			_, err := client.Emails().Send(context.Background(), email)

			require.Equal(t, "delivery_unavailable", requireEmailError(t, err).Code)
			require.Len(t, server.received(), tt.wantRequests)
		})
	}
}

func TestEmailsSendLongRetryAfterFailsAtOnce(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusTooManyRequests, body: `{"code":"throttled","detail":"Slow down."}`, header: map[string]string{"Retry-After": "60"}}, accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), templateEmail())

	emailErr := requireEmailError(t, err)
	require.Equal(t, "throttled", emailErr.Code)
	require.Equal(t, EmailErrorRateLimitExceeded, emailErr.Name)
	require.Equal(t, 60*time.Second, emailErr.RetryAfter)
	require.Len(t, server.received(), 1)
}

func TestEmailsSendWaitsForRetryAfter(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusTooManyRequests, body: `{"code":"throttled"}`, header: map[string]string{"Retry-After": "1"}}, accepted("msg_1"))
	client := newEmailClient(t, server)

	started := time.Now()
	result, err := client.Emails().Send(context.Background(), templateEmail())

	require.NoError(t, err)
	require.Equal(t, "msg_1", result.ID)
	require.GreaterOrEqual(t, time.Since(started), time.Second)
}

func TestEmailsSendErrorFields(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusBadRequest, body: `{
		"type": "validation_error",
		"code": "sender_not_verified",
		"detail": "The sender's domain is not verified.",
		"attr": "from",
		"why": "PostHog sends only from verified domains.",
		"fix": "Verify example.org in PostHog.",
		"docs_url": "https://posthog.com/docs/workflows/email"
	}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), templateEmail())

	require.Equal(t, &EmailError{
		Name:       EmailErrorValidation,
		Code:       "sender_not_verified",
		Message:    "The sender's domain is not verified.",
		StatusCode: http.StatusBadRequest,
		Field:      "from",
		Why:        "PostHog sends only from verified domains.",
		Fix:        "Verify example.org in PostHog.",
		DocsURL:    "https://posthog.com/docs/workflows/email",
	}, requireEmailError(t, err))
	require.Equal(t, "posthog emails: sender_not_verified (HTTP 400): The sender's domain is not verified.", err.Error())
}

func TestEmailsErrorNames(t *testing.T) {
	tests := []struct {
		status int
		code   string
		want   EmailErrorName
	}{
		{http.StatusBadRequest, "invalid_recipient", EmailErrorValidation},
		{http.StatusUnauthorized, "authentication_failed", EmailErrorMissingAPIKey},
		{http.StatusForbidden, "permission_denied", EmailErrorInvalidPermission},
		{http.StatusNotFound, "not_found", EmailErrorNotFound},
		{http.StatusConflict, "template_outdated", EmailErrorValidation},
		{http.StatusConflict, "idempotency_key_in_flight", EmailErrorConcurrentIdempotentRequests},
		{http.StatusUnprocessableEntity, "idempotency_key_reused", EmailErrorInvalidIdempotentRequest},
		{http.StatusUnprocessableEntity, "recipient_suppressed", EmailErrorValidation},
		{http.StatusTooManyRequests, "quota_limited", EmailErrorMonthlyQuotaExceeded},
		{http.StatusTooManyRequests, "throttled", EmailErrorRateLimitExceeded},
		{http.StatusServiceUnavailable, "delivery_unavailable", EmailErrorServiceUnavailable},
		{http.StatusInternalServerError, "internal", EmailErrorApplication},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			server := newEmailServer(t, failed(tt.status, tt.code))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), TemplateEmail{To: "user@example.com", TemplateID: testTemplateID, MaxRetries: Ptr(0)})

			emailErr := requireEmailError(t, err)
			require.Equal(t, tt.code, emailErr.Code)
			require.Equal(t, tt.want, emailErr.Name)
			require.Equal(t, tt.status, emailErr.StatusCode)
		})
	}
}

func TestEmailsSendAnswersWithoutACode(t *testing.T) {
	tests := []struct {
		name  string
		reply stubReply
		want  EmailError
	}{
		{
			name:  "accepted without a message ID",
			reply: stubReply{status: http.StatusAccepted, body: `{"message_id": ""}`},
			want:  EmailError{Name: EmailErrorApplication, Code: "api_error", Message: "PostHog answered without a message ID.", StatusCode: http.StatusAccepted},
		},
		{
			name:  "proxy error page",
			reply: stubReply{status: http.StatusBadGateway, body: "<html>Bad gateway</html>"},
			want:  EmailError{Name: EmailErrorApplication, Code: "api_error", Message: "PostHog answered HTTP 502.", StatusCode: http.StatusBadGateway},
		},
		{
			name:  "redirect",
			reply: stubReply{status: http.StatusFound, header: map[string]string{"Location": "https://elsewhere.example.com/"}},
			want:  EmailError{Name: EmailErrorApplication, Code: "api_error", Message: "PostHog answered HTTP 302.", StatusCode: http.StatusFound},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, tt.reply)
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), templateEmail())

			require.Equal(t, &tt.want, requireEmailError(t, err))
			require.Len(t, server.received(), 1)
		})
	}
}

func TestEmailsErrorsNeverHoldTheSecretKeyOrBody(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	client, err := NewWithConfig("phc_test", Config{SecretKey: "phs_very_secret", Transport: transport, EmailRequestMaxRetries: Ptr(0)})
	require.NoError(t, err)
	defer client.Close()

	_, err = client.Emails().Send(context.Background(), CodeEmail{To: "user@example.com", TemplateKey: "reset", Subject: "Reset", HTML: "<p>token-abc</p>"})

	emailErr := requireEmailError(t, err)
	require.Equal(t, "network_error", emailErr.Code)
	require.Equal(t, EmailErrorServiceUnavailable, emailErr.Name)
	require.NotContains(t, err.Error(), "phs_very_secret")
	require.NotContains(t, err.Error(), "token-abc")
}

func TestEmailsSendHonorsTheRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isEmailPath(r.URL.Path) {
			<-release
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := NewWithConfig("phc_test", Config{Endpoint: server.URL, AppHost: server.URL, SecretKey: "phs_test", EmailRequestTimeout: 50 * time.Millisecond, EmailRequestMaxRetries: Ptr(0)})
	require.NoError(t, err)

	_, err = client.Emails().Send(context.Background(), templateEmail())

	emailErr := requireEmailError(t, err)
	require.Equal(t, "network_error", emailErr.Code)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEmailsSendStopsWhenTheContextEnds(t *testing.T) {
	server := newEmailServer(t, failed(http.StatusServiceUnavailable, "delivery_unavailable"), accepted("msg_1"))
	client := newEmailClient(t, server)
	senderOf(client).backoff = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Emails().Send(ctx, templateEmail())

	require.Equal(t, "delivery_unavailable", requireEmailError(t, err).Code)
	require.Len(t, server.received(), 1)
}

func TestEmailsGet(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusOK, body: `{
		"id": "01a18356-c56c-0001-e494-7666bc915188",
		"status": "bounced",
		"created_at": "2026-10-28T16:27:44.364000Z",
		"status_at": "2026-10-28T16:30:02.100000Z",
		"reason": "hard"
	}`})
	client := newEmailClient(t, server)

	status, err := client.Emails().Get(context.Background(), "01A18356-C56C-0001-E494-7666BC915188")

	require.NoError(t, err)
	require.Equal(t, &EmailStatus{
		ID:        testMessageID,
		Status:    "bounced",
		CreatedAt: time.Date(2026, 10, 28, 16, 27, 44, 364000000, time.UTC),
		StatusAt:  time.Date(2026, 10, 28, 16, 30, 2, 100000000, time.UTC),
		Reason:    "hard",
	}, status)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodGet, requests[0].Method)
	require.Equal(t, "/api/projects/@current/messaging/emails/"+testMessageID+"/", requests[0].Path)
	require.Equal(t, "Bearer phs_test", requests[0].Header.Get("Authorization"))
	require.Empty(t, requests[0].Header.Get("Idempotency-Key"))
	require.Empty(t, requests[0].Header.Get("Content-Type"))
}

func TestEmailsGetPassesAnUnknownStatusThrough(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusOK, body: `{"id":"` + testMessageID + `","status":"archived","created_at":"2026-10-28T16:27:44Z","status_at":"2026-10-28T16:27:44Z","reason":null}`})
	client := newEmailClient(t, server)

	status, err := client.Emails().Get(context.Background(), testMessageID)

	require.NoError(t, err)
	require.Equal(t, "archived", status.Status)
	require.Empty(t, status.Reason)
}

func TestEmailsGetErrors(t *testing.T) {
	tests := []struct {
		name     string
		replies  []stubReply
		code     string
		requests int
	}{
		{name: "unknown message ID", replies: []stubReply{failed(http.StatusNotFound, "not_found")}, code: "not_found", requests: 1},
		{name: "incomplete record", replies: []stubReply{{status: http.StatusOK, body: `{"id":"` + testMessageID + `","status":"sent"}`}}, code: "api_error", requests: 1},
		{name: "time that is not ISO 8601", replies: []stubReply{{status: http.StatusOK, body: `{"id":"` + testMessageID + `","status":"sent","created_at":"yesterday","status_at":"2026-10-28T16:27:44Z"}`}}, code: "api_error", requests: 1},
		{name: "unavailable then found", replies: []stubReply{failed(http.StatusServiceUnavailable, "unavailable"), failed(http.StatusNotFound, "not_found")}, code: "not_found", requests: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, tt.replies...)
			client := newEmailClient(t, server)

			_, err := client.Emails().Get(context.Background(), testMessageID)

			require.Equal(t, tt.code, requireEmailError(t, err).Code)
			require.Len(t, server.received(), tt.requests)
		})
	}
}

func TestEmailsGetChecksTheMessageID(t *testing.T) {
	server := newEmailServer(t)
	client := newEmailClient(t, server)

	_, err := client.Emails().Get(context.Background(), "msg_1")

	emailErr := requireEmailError(t, err)
	require.Equal(t, "invalid_message_id", emailErr.Code)
	require.Equal(t, EmailErrorValidation, emailErr.Name)
	require.Empty(t, server.received())
}

func TestEmailsDisabledClient(t *testing.T) {
	client := New("")

	_, sendErr := client.Emails().Send(context.Background(), templateEmail())
	_, getErr := client.Emails().Get(context.Background(), testMessageID)

	require.Equal(t, "client_disabled", requireEmailError(t, sendErr).Code)
	require.Equal(t, "client_disabled", requireEmailError(t, getErr).Code)
	require.ErrorIs(t, sendErr, ErrSDKDisabled)
}

func TestEmailsClosedClient(t *testing.T) {
	server := newEmailServer(t)
	client := newEmailClient(t, server)
	require.NoError(t, client.Close())

	_, sendErr := client.Emails().Send(context.Background(), templateEmail())
	_, getErr := client.Emails().Get(context.Background(), testMessageID)

	require.Equal(t, "client_closed", requireEmailError(t, sendErr).Code)
	require.Equal(t, "client_closed", requireEmailError(t, getErr).Code)
	require.ErrorIs(t, sendErr, ErrClosed)
	require.Empty(t, server.received())
}

func TestEmailsCloseWaitsForASendInItsRetryBackoff(t *testing.T) {
	server := newEmailServer(t, failed(http.StatusServiceUnavailable, "delivery_unavailable"), accepted("msg_1"))
	client := newEmailClient(t, server)
	retrying := make(chan struct{})
	senderOf(client).backoff = func(int) time.Duration {
		close(retrying)
		return 200 * time.Millisecond
	}
	sent := make(chan error, 1)
	go func() {
		_, err := client.Emails().Send(context.Background(), templateEmail())
		sent <- err
	}()
	<-retrying

	require.NoError(t, client.Close())

	// The retry's request reached the server before Close returned.
	require.Len(t, server.received(), 2, "Close returned before the send settled")
	select {
	case err := <-sent:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the send never returned")
	}
}

func TestEmailsCloseKeepsToItsTimeoutDuringARetryBackoff(t *testing.T) {
	server := newEmailServer(t, failed(http.StatusServiceUnavailable, "delivery_unavailable"), accepted("msg_1"))
	client := newEmailClient(t, server)
	retrying := make(chan struct{})
	senderOf(client).backoff = func(int) time.Duration {
		close(retrying)
		return time.Hour
	}
	sent := make(chan error, 1)
	go func() {
		_, err := client.Emails().Send(context.Background(), templateEmail())
		sent <- err
	}()
	<-retrying
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := client.CloseWithContext(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, "delivery_unavailable", requireEmailError(t, <-sent).Code)
	require.Len(t, server.received(), 1)
}

func TestConfigRejectsInvalidEmailOptions(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		field  string
	}{
		{name: "negative timeout", config: Config{EmailRequestTimeout: -time.Second}, field: "EmailRequestTimeout"},
		{name: "negative retries", config: Config{EmailRequestMaxRetries: Ptr(-1)}, field: "EmailRequestMaxRetries"},
		{name: "migrated from with a space", config: Config{MigratedFrom: "send grid"}, field: "MigratedFrom"},
		{name: "migrated from with a line break", config: Config{MigratedFrom: "resend\r\nX-Evil: 1"}, field: "MigratedFrom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWithConfig("phc_test", tt.config)

			var configErr ConfigError
			require.True(t, errors.As(err, &configErr), "want ConfigError, got %v", err)
			require.Equal(t, tt.field, configErr.Field)
		})
	}
}

func isEmailPath(path string) bool { return strings.Contains(path, "/messaging/") }

// roundTripFunc answers email requests, one at a time, and refuses every other
// request, such as the flag poller's.
type roundTripFunc func(*http.Request) (*http.Response, error)

var roundTripMu sync.Mutex

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	if !isEmailPath(r.URL.Path) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}
	roundTripMu.Lock()
	defer roundTripMu.Unlock()
	return f(r)
}

type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Debugf(string, ...interface{}) {}
func (l *recordingLogger) Logf(string, ...interface{})   {}
func (l *recordingLogger) Errorf(string, ...interface{}) {}
func (l *recordingLogger) Warnf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}
