package posthog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type batchRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]interface{}
	Size   int
}

// recipients are the recipients a batch request carries.
func (r batchRequest) recipients() []map[string]interface{} {
	items, _ := r.Body["recipients"].([]interface{})
	recipients := make([]map[string]interface{}, len(items))
	for i, item := range items {
		recipients[i], _ = item.(map[string]interface{})
	}
	return recipients
}

// batchServer answers each batch send with answer, which sees the requests
// before it, and records what it received. Any other email request fails the
// test.
type batchServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []batchRequest
}

// dropConnection makes answer close the connection without an answer, as a
// lost answer does.
const dropConnection = -1

func newBatchServer(t *testing.T, answer func(n int, r batchRequest) stubReply) *batchServer {
	s := &batchServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		if route != "POST /api/messaging/send/email_batch/" {
			if isEmailPath(r.URL.Path) {
				t.Errorf("unexpected email request %s", route)
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("the request body isn't JSON: %v", err)
			}
		}
		request := batchRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body, Size: len(raw)}
		s.mu.Lock()
		n := len(s.requests)
		s.requests = append(s.requests, request)
		s.mu.Unlock()
		reply := answer(n, request)
		if reply.status == dropConnection {
			panic(http.ErrAbortHandler)
		}
		for name, value := range reply.header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *batchServer) received() []batchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]batchRequest(nil), s.requests...)
}

func messageIDFor(key string) string { return "msg-" + key }

const wantResendFix = "Send it again with the same IdempotencyKey within 24 hours of the first request that used it, so it goes out once."

// slots answers each recipient of a batch request with the slot slot makes.
func slots(r batchRequest, slot func(recipient map[string]interface{}) string, warnings string) stubReply {
	parts := make([]string, 0)
	for _, recipient := range r.recipients() {
		parts = append(parts, slot(recipient))
	}
	return stubReply{status: http.StatusOK, body: `{"results":[` + strings.Join(parts, ",") + `]` + warnings + `}`}
}

func queuedSlot(recipient map[string]interface{}) string {
	return `{"message_id":"` + messageIDFor(recipient["idempotency_key"].(string)) + `","status":"queued","replayed":false}`
}

func refusedSlot(code string, status int) func(map[string]interface{}) string {
	return func(recipient map[string]interface{}) string {
		return fmt.Sprintf(`{"message_id":"%s","error":{"code":"%s","detail":"PostHog said no.","status":%d,"retryable":false}}`, messageIDFor(recipient["idempotency_key"].(string)), code, status)
	}
}

func queuedAll(_ int, r batchRequest) stubReply { return slots(r, queuedSlot, "") }

func recipientsTo(n int) []BatchRecipient {
	recipients := make([]BatchRecipient, n)
	for i := range recipients {
		recipients[i] = BatchRecipient{To: fmt.Sprintf("user%d@example.com", i), IdempotencyKey: fmt.Sprintf("digest:%d", i)}
	}
	return recipients
}

func templateBatch(recipients ...BatchRecipient) TemplateEmailBatch {
	return TemplateEmailBatch{TemplateID: testTemplateID, Recipients: recipients}
}

func statuses(result *SendBatchResult) []string {
	out := make([]string, len(result.Results))
	for i, r := range result.Results {
		out[i] = r.Status
	}
	return out
}

func TestEmailsBatchTemplate(t *testing.T) {
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		return stubReply{status: http.StatusOK, body: `{"results":[` +
			`{"message_id":"m-1","status":"queued","replayed":false},` +
			`{"message_id":"m-2","status":"queued","replayed":true}]}`}
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), TemplateEmailBatch{
		TemplateID:   strings.ToUpper(testTemplateID),
		TemplateData: map[string]interface{}{"product": "Acme", "name": "there"},
		Recipients: []BatchRecipient{
			{To: "ada@example.com", DistinctID: "user_1", IdempotencyKey: "digest:ada", TemplateData: map[string]interface{}{"name": "Ada"}},
			{To: "alan@example.com"},
		},
	})

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, "/api/messaging/send/email_batch/", requests[0].Path)
	require.Equal(t, "Bearer phs_test", requests[0].Header.Get("Authorization"))
	require.Equal(t, "application/json", requests[0].Header.Get("Content-Type"))
	require.Empty(t, requests[0].Header.Get("Idempotency-Key"))
	madeKey := requests[0].recipients()[1]["idempotency_key"].(string)
	require.NoError(t, uuid.Validate(madeKey))
	require.Equal(t, map[string]interface{}{
		"template_id":  testTemplateID,
		"message_data": map[string]interface{}{"product": "Acme", "name": "there"},
		"recipients": []interface{}{
			map[string]interface{}{"to": "ada@example.com", "distinct_id": "user_1", "idempotency_key": "digest:ada", "message_data": map[string]interface{}{"name": "Ada"}},
			map[string]interface{}{"to": "alan@example.com", "idempotency_key": madeKey},
		},
	}, requests[0].Body)
	require.Equal(t, &SendBatchResult{
		Results: []BatchEmailResult{
			{Index: 0, To: "ada@example.com", IdempotencyKey: "digest:ada", Status: "queued", MessageID: "m-1"},
			{Index: 1, To: "alan@example.com", IdempotencyKey: madeKey, Status: "queued", MessageID: "m-2", Replayed: true},
		},
		Warnings: []EmailWarning{},
	}, result)
}

func TestEmailsBatchCode(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)
	sensitive := true

	result, err := client.Emails().Batch(context.Background(), CodeEmailBatch{
		TemplateKey: "price-change",
		Subject:     "Prices change on <May 1>",
		HTML:        "<p>New prices</p>",
		Text:        "New prices",
		From:        "Acme <billing@example.com>",
		ReplyTo:     []string{"support@example.com"},
		Sensitive:   &sensitive,
		Tracking:    EmailTrackingOff(),
		Recipients:  recipientsTo(2),
	})

	require.NoError(t, err)
	require.Equal(t, []string{"queued", "queued"}, statuses(result))
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, map[string]interface{}{
		"template_key": "price-change",
		"subject":      "Prices change on <May 1>",
		"html":         "<p>New prices</p>",
		"text":         "New prices",
		"from":         "Acme <billing@example.com>",
		"reply_to":     []interface{}{"support@example.com"},
		"sensitive":    true,
		"tracking":     false,
		"recipients": []interface{}{
			map[string]interface{}{"to": "user0@example.com", "idempotency_key": "digest:0"},
			map[string]interface{}{"to": "user1@example.com", "idempotency_key": "digest:1"},
		},
	}, requests[0].Body)
}

func TestEmailsBatchRefusesBeforeAnyRequest(t *testing.T) {
	tooMany := 10
	tests := []struct {
		name    string
		batch   EmailBatch
		code    string
		field   string
		message string
	}{
		{name: "no batch", batch: nil, code: "invalid_form", message: "Batch needs a TemplateEmailBatch or a CodeEmailBatch."},
		{name: "a nil batch", batch: (*TemplateEmailBatch)(nil), code: "invalid_form", message: "Batch needs a TemplateEmailBatch or a CodeEmailBatch."},
		{name: "no recipients", batch: templateBatch(), code: "invalid_recipients", field: "Recipients", message: "Recipients must hold at least one recipient."},
		{name: "a recipient without an address", batch: templateBatch(BatchRecipient{To: "a@example.com"}, BatchRecipient{To: " "}), code: "invalid_recipient", field: "Recipients[1].To", message: "Recipients[1]: To must be one email address."},
		{name: "a bad key", batch: templateBatch(BatchRecipient{To: "a@example.com", IdempotencyKey: "has space"}), code: "invalid_idempotency_key", field: "Recipients[0].IdempotencyKey", message: "Recipients[0]: IdempotencyKey must be 1 to 255 visible ASCII characters without spaces."},
		{name: "a repeated key", batch: templateBatch(BatchRecipient{To: "a@example.com", IdempotencyKey: "k"}, BatchRecipient{To: "b@example.com"}, BatchRecipient{To: "c@example.com", IdempotencyKey: "k"}), code: "invalid_idempotency_key", field: "Recipients[2].IdempotencyKey", message: "Recipients[2] has the same IdempotencyKey as Recipients[0]. Give each recipient its own key."},
		{name: "a bad template ID", batch: TemplateEmailBatch{TemplateID: "welcome", Recipients: recipientsTo(1)}, code: "invalid_template_id", field: "TemplateID", message: "TemplateID must be the template's UUID."},
		{name: "template data that isn't JSON", batch: TemplateEmailBatch{TemplateID: testTemplateID, Recipients: []BatchRecipient{{To: "a@example.com", TemplateData: map[string]interface{}{"f": func() {}}}}}, code: "invalid_template_data", field: "Recipients[0].TemplateData", message: "Recipients[0]: TemplateData must encode as JSON."},
		{name: "template data in a code batch", batch: CodeEmailBatch{TemplateKey: "k", Subject: "s", Text: "t", Recipients: []BatchRecipient{{To: "a@example.com", TemplateData: map[string]interface{}{"name": "Ada"}}}}, code: "invalid_form", field: "Recipients[0].TemplateData", message: "Recipients[0] has TemplateData, but a CodeEmailBatch sends the same content to every recipient. Send personalized content with a TemplateEmailBatch or with Send."},
		{name: "a bad template key", batch: CodeEmailBatch{TemplateKey: "a b", Subject: "s", Text: "t", Recipients: recipientsTo(1)}, code: "invalid_template_key", field: "TemplateKey"},
		{name: "no subject", batch: CodeEmailBatch{TemplateKey: "k", Text: "t", Recipients: recipientsTo(1)}, code: "invalid_form", field: "Subject", message: "A CodeEmailBatch needs a Subject."},
		{name: "no content", batch: CodeEmailBatch{TemplateKey: "k", Subject: "s", Recipients: recipientsTo(1)}, code: "invalid_form", field: "HTML", message: "A CodeEmailBatch needs HTML, Text or both."},
		{name: "a blank reply-to", batch: CodeEmailBatch{TemplateKey: "k", Subject: "s", Text: "t", ReplyTo: []string{""}, Recipients: recipientsTo(1)}, code: "invalid_reply_to", field: "ReplyTo"},
		{name: "too many retries", batch: TemplateEmailBatch{TemplateID: testTemplateID, Recipients: recipientsTo(1), MaxRetries: &tooMany}, code: "invalid_max_retries", field: "MaxRetries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, queuedAll)
			client := newEmailClientFor(t, server.URL)

			result, err := client.Emails().Batch(context.Background(), tt.batch)

			require.Nil(t, result)
			emailErr := requireEmailError(t, err)
			require.Equal(t, tt.code, emailErr.Code)
			require.Equal(t, tt.field, emailErr.Field)
			if tt.message != "" {
				require.Equal(t, tt.message, emailErr.Message)
			}
			require.Empty(t, server.received())
		})
	}
}

func TestEmailsBatchRefusesMoreThan100RecipientsBeforeAnyRequest(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(101)...))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "invalid_recipients", emailErr.Code)
	require.Equal(t, "Recipients", emailErr.Field)
	require.Nil(t, result)
	require.Empty(t, server.received())
}

func TestEmailsBatchSendsOneRequestOf100Recipients(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(100)...))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].recipients(), 100)
	for i, r := range result.Results {
		require.Equal(t, i, r.Index)
		require.Equal(t, "queued", r.Status)
	}
}

func TestEmailsBatchReturnsATooLargeRequestAsTheError(t *testing.T) {
	tests := []struct {
		name, body, fix string
		status          int
		recipients      int
	}{
		{
			name:   "a 413 without a fix",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"code":"content_too_large","detail":"The batch is larger than PostHog takes."}`,
			fix:    "Split the recipients into smaller batches.",
		},
		{
			name:   "a 413 with PostHog's fix",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"code":"content_too_large","detail":"The batch is larger than PostHog takes.","fix":"Send fewer recipients in each batch."}`,
			fix:    "Send fewer recipients in each batch.",
		},
		{
			name:       "a 413 for one recipient",
			status:     http.StatusRequestEntityTooLarge,
			body:       `{"code":"content_too_large","detail":"The batch is larger than PostHog takes."}`,
			fix:        "Make this recipient's email smaller.",
			recipients: 1,
		},
		{
			name:   "shared content too large, which splitting doesn't help",
			status: http.StatusBadRequest,
			body:   `{"code":"content_too_large","detail":"The shared content is larger than PostHog takes."}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, func(int, batchRequest) stubReply { return stubReply{status: tt.status, body: tt.body} })
			client := newEmailClientFor(t, server.URL)
			count := tt.recipients
			if count == 0 {
				count = 4
			}

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(count)...))

			emailErr := requireEmailError(t, err)
			require.Equal(t, "content_too_large", emailErr.Code)
			require.Equal(t, tt.status, emailErr.StatusCode)
			require.Equal(t, tt.fix, emailErr.Fix)
			require.Len(t, server.received(), 1, "the client doesn't split the batch")
			for _, status := range statuses(result) {
				require.Equal(t, "failed", status)
			}
			require.Len(t, result.Results, count)
		})
	}
}

func TestEmailsBatchWaitsLongerThanASend(t *testing.T) {
	for _, tt := range []struct {
		name          string
		clientTimeout time.Duration
		want          time.Duration
	}{
		{name: "a short client timeout", clientTimeout: 20 * time.Millisecond, want: 45 * time.Second},
		{name: "a longer client timeout", clientTimeout: time.Minute, want: time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, queuedAll)
			var waits []time.Duration
			deadlines := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if deadline, ok := r.Context().Deadline(); ok {
					waits = append(waits, time.Until(deadline))
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			client := newEmailClientFor(t, server.URL, func(config *Config) {
				config.EmailRequestTimeout = tt.clientTimeout
				config.Transport = deadlines
			})

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

			require.NoError(t, err)
			require.Equal(t, []string{"queued"}, statuses(result))
			require.Len(t, waits, 1)
			require.InDelta(t, tt.want.Seconds(), waits[0].Seconds(), 1)
		})
	}
}

func TestEmailsBatchSettlesEachRecipient(t *testing.T) {
	refused := refusedSlot("recipient_suppressed", http.StatusUnprocessableEntity)
	tests := []struct {
		name      string
		answers   []func(r batchRequest) stubReply
		status    string
		errorCode string
	}{
		{
			name:      "a refusal",
			answers:   []func(batchRequest) stubReply{func(r batchRequest) stubReply { return slots(r, refused, "") }},
			status:    "failed",
			errorCode: "recipient_suppressed",
		},
		{
			name: "a refusal after a lost answer",
			answers: []func(batchRequest) stubReply{
				func(batchRequest) stubReply { return stubReply{status: dropConnection} },
				func(r batchRequest) stubReply { return slots(r, refused, "") },
			},
			status:    "unconfirmed",
			errorCode: "recipient_suppressed",
		},
		{
			name: "a refusal after a 503",
			answers: []func(batchRequest) stubReply{
				func(batchRequest) stubReply { return failed(http.StatusServiceUnavailable, "delivery_unavailable") },
				func(r batchRequest) stubReply { return slots(r, refused, "") },
			},
			status:    "unconfirmed",
			errorCode: "recipient_suppressed",
		},
		{
			name: "a reused key after a lost answer",
			answers: []func(batchRequest) stubReply{
				func(batchRequest) stubReply { return stubReply{status: dropConnection} },
				func(r batchRequest) stubReply {
					return slots(r, refusedSlot("idempotency_key_reused", http.StatusConflict), "")
				},
			},
			status:    "failed",
			errorCode: "idempotency_key_reused",
		},
		{
			name: "queued after a lost answer",
			answers: []func(batchRequest) stubReply{
				func(batchRequest) stubReply { return stubReply{status: dropConnection} },
				func(r batchRequest) stubReply { return slots(r, queuedSlot, "") },
			},
			status: "queued",
		},
		{
			name: "a queued slot with a null error",
			answers: []func(batchRequest) stubReply{func(r batchRequest) stubReply {
				return slots(r, func(recipient map[string]interface{}) string {
					return `{"message_id":"` + messageIDFor(recipient["idempotency_key"].(string)) + `","status":"queued","replayed":false,"error":null}`
				}, "")
			}},
			status: "queued",
		},
		{
			name:      "a slot the pipeline didn't confirm",
			answers:   []func(batchRequest) stubReply{func(r batchRequest) stubReply { return slots(r, refusedSlot("delivery_unavailable", 503), "") }},
			status:    "unconfirmed",
			errorCode: "delivery_unavailable",
		},
		{
			name:      "a slot another request still holds",
			answers:   []func(batchRequest) stubReply{func(r batchRequest) stubReply { return slots(r, refusedSlot("idempotency_key_in_flight", 409), "") }},
			status:    "unconfirmed",
			errorCode: "idempotency_key_in_flight",
		},
		{
			name: "a slot that isn't an object",
			answers: []func(batchRequest) stubReply{func(r batchRequest) stubReply {
				return slots(r, func(map[string]interface{}) string { return `{"message_id":"m-1","error":"no"}` }, "")
			}},
			status:    "unconfirmed",
			errorCode: "api_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, func(n int, r batchRequest) stubReply { return tt.answers[n](r) })
			client := newEmailClientFor(t, server.URL)

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

			require.NoError(t, err)
			require.Len(t, server.received(), len(tt.answers))
			got := result.Results[0]
			require.Equal(t, tt.status, got.Status)
			if tt.errorCode == "" {
				require.Nil(t, got.Error)
				require.Equal(t, messageIDFor("digest:0"), got.MessageID)
				return
			}
			require.Equal(t, tt.errorCode, got.Error.Code)
			require.NotEmpty(t, got.MessageID)
			if strings.Contains(tt.name, "refusal") || strings.Contains(tt.name, "reused") {
				require.Equal(t, "PostHog said no.", got.Error.Message, "PostHog's detail is kept")
			}
			if tt.status == "unconfirmed" {
				require.Equal(t, wantResendFix, got.Error.Fix)
			}
		})
	}
	t.Run("a refusal names its status and message ID", func(t *testing.T) {
		server := newBatchServer(t, func(_ int, r batchRequest) stubReply { return slots(r, refused, "") })
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

		require.NoError(t, err)
		require.Equal(t, &EmailError{Name: EmailErrorValidation, Code: "recipient_suppressed", Message: "PostHog said no.", StatusCode: http.StatusUnprocessableEntity, MessageID: messageIDFor("digest:0")}, result.Results[0].Error)
	})
}

func TestEmailsBatchKeepsRecipientsUnconfirmedAfterALostAnswer(t *testing.T) {
	tests := []struct {
		name, callCode, errorCode string
		last                      stubReply
	}{
		{name: "a rate limit", callCode: "rate_limited", errorCode: "delivery_unavailable", last: stubReply{status: http.StatusTooManyRequests, body: `{"code":"rate_limited","detail":"Slow down."}`}},
		{name: "a refusal", callCode: "quota_limited", errorCode: "delivery_unavailable", last: stubReply{status: http.StatusTooManyRequests, body: `{"code":"quota_limited","detail":"Over quota."}`}},
		{name: "keys still in flight", callCode: "idempotency_key_in_flight", errorCode: "idempotency_key_in_flight", last: failed(http.StatusConflict, "idempotency_key_in_flight")},
		{name: "a 503", callCode: "delivery_unavailable", errorCode: "delivery_unavailable", last: failed(http.StatusServiceUnavailable, "delivery_unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, func(n int, r batchRequest) stubReply {
				if n == 0 {
					return stubReply{status: dropConnection}
				}
				return tt.last
			})
			client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(1) })

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

			require.Equal(t, tt.callCode, requireEmailError(t, err).Code)
			require.Len(t, server.received(), 2, "a send and its retry, nothing more")
			for i, got := range result.Results {
				require.Equal(t, "unconfirmed", got.Status, "the lost send may have queued it")
				require.Equal(t, tt.errorCode, got.Error.Code)
				require.Equal(t, wantResendFix, got.Error.Fix)
				require.Equal(t, fmt.Sprintf("digest:%d", i), got.IdempotencyKey)
			}
		})
	}
}

func TestEmailsBatchLeavesUnconfirmedEmailsToAResendWithTheirKeys(t *testing.T) {
	t.Run("after a batch that fails", func(t *testing.T) {
		server := newBatchServer(t, func(int, batchRequest) stubReply { return stubReply{status: dropConnection} })
		client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(1) })

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

		require.Equal(t, "network_error", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 2, "a send and its retry, nothing more")
		for i, got := range result.Results {
			require.Equal(t, "unconfirmed", got.Status)
			require.Equal(t, fmt.Sprintf("digest:%d", i), got.IdempotencyKey)
			require.Empty(t, got.MessageID)
			require.Equal(t, "delivery_unavailable", got.Error.Code)
			require.Equal(t, wantResendFix, got.Error.Fix)
		}
	})
	t.Run("after a batch that succeeds", func(t *testing.T) {
		server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
			return slots(r, func(recipient map[string]interface{}) string {
				if recipient["idempotency_key"] == "digest:1" {
					return `{"error":{"code":"delivery_unavailable","detail":"PostHog couldn't confirm this email.","status":503,"retryable":true}}`
				}
				return queuedSlot(recipient)
			}, "")
		})
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

		require.NoError(t, err)
		require.Len(t, server.received(), 1)
		require.Equal(t, []string{"queued", "unconfirmed"}, statuses(result))
		got := result.Results[1]
		require.Equal(t, "digest:1", got.IdempotencyKey)
		require.Empty(t, got.MessageID)
		require.Equal(t, wantResendFix, got.Error.Fix)
	})
}

func TestEmailsBatchTakesKeysInFlightOnlyFromA409(t *testing.T) {
	server := newBatchServer(t, func(int, batchRequest) stubReply {
		return failed(http.StatusUnauthorized, "idempotency_key_in_flight")
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	require.Equal(t, http.StatusUnauthorized, requireEmailError(t, err).StatusCode)
	require.Len(t, server.received(), 1)
	require.Equal(t, []string{"failed", "failed"}, statuses(result), "a 401 refused the request before PostHog queued anything")
}

func TestEmailsBatchKeepsRecipientsUnconfirmedWhileTheirKeysAreInFlight(t *testing.T) {
	server := newBatchServer(t, func(int, batchRequest) stubReply {
		return failed(http.StatusConflict, "idempotency_key_in_flight")
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(0) })

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	require.Equal(t, "idempotency_key_in_flight", requireEmailError(t, err).Code)
	require.Equal(t, []string{"unconfirmed", "unconfirmed"}, statuses(result), "an earlier request with these keys may still queue them")
	for _, r := range result.Results {
		require.Equal(t, http.StatusConflict, r.Error.StatusCode)
		require.Equal(t, wantResendFix, r.Error.Fix)
	}
	require.Len(t, server.received(), 1)
}

func TestEmailsBatchUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		reply   stubReply
		code    string
		errName EmailErrorName
		why     string
		fix     string
	}{
		{
			name:    "batch_unavailable",
			reply:   stubReply{status: http.StatusConflict, body: `{"code":"batch_unavailable","detail":"Batch sends aren't available for this project yet.","fix":"Send each email on its own."}`},
			code:    "batch_unavailable",
			errName: EmailErrorApplication,
			fix:     "Until batch sends are on for the project, send each email the batch didn't queue with Emails.Send and its recipient's IdempotencyKey.",
		},
		{
			name:    "a 404 without a body",
			reply:   stubReply{status: http.StatusNotFound, body: `<html>Not found</html>`},
			code:    "not_found",
			errName: EmailErrorNotFound,
			why:     "The template doesn't exist, or batch sends are off for this project or this PostHog version.",
			fix:     "Check TemplateID, or send each email the batch didn't queue with Emails.Send and its recipient's IdempotencyKey.",
		},
		{
			name:    "a 404 with PostHog's error body",
			reply:   stubReply{status: http.StatusNotFound, body: `{"code":"not_found","detail":"Not found."}`},
			code:    "not_found",
			errName: EmailErrorNotFound,
			fix:     "Check TemplateID, or send each email the batch didn't queue with Emails.Send and its recipient's IdempotencyKey.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newBatchServer(t, func(int, batchRequest) stubReply { return tt.reply })
			client := newEmailClientFor(t, server.URL)

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

			emailErr := requireEmailError(t, err)
			require.Equal(t, tt.code, emailErr.Code)
			require.Equal(t, tt.errName, emailErr.Name)
			require.Equal(t, tt.why, emailErr.Why)
			require.Equal(t, tt.fix, emailErr.Fix)
			require.Len(t, server.received(), 1, "not retried")
			require.Equal(t, []string{"failed", "failed"}, statuses(result))
		})
	}
}

func TestEmailsBatchAnswerWithoutAResultPerRecipient(t *testing.T) {
	server := newBatchServer(t, func(int, batchRequest) stubReply {
		return stubReply{status: http.StatusOK, body: `{"results":[{"message_id":"m-1","status":"queued"}]}`}
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "api_error", emailErr.Code)
	require.Equal(t, http.StatusOK, emailErr.StatusCode)
	require.Len(t, server.received(), 1, "not retried")
	require.Equal(t, []string{"unconfirmed", "unconfirmed"}, statuses(result))
	for _, r := range result.Results {
		require.Equal(t, "delivery_unavailable", r.Error.Code)
	}
}

func TestEmailsBatchReturnsWarnings(t *testing.T) {
	warning := `,"warnings":[{"code":"sandbox_sender","message":"Sent from the PostHog test sender."}]`
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply { return slots(r, queuedSlot, warning) })
	logs := &recordingLogger{}
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.Logger = logs })

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	require.NoError(t, err)
	require.Equal(t, []EmailWarning{{Code: "sandbox_sender", Message: "Sent from the PostHog test sender."}}, result.Warnings)
	require.Equal(t, []string{"PostHog email warning sandbox_sender: Sent from the PostHog test sender."}, logs.warnings())
}

// closingLogger reports when Close has shut the lanes down, which it does
// only after it stopped new email requests and before it waits for them.
type closingLogger struct {
	testLogger
	once     sync.Once
	shutDown chan struct{}
}

func (l *closingLogger) Debugf(format string, args ...interface{}) {
	if strings.Contains(format, "shutdown completed") {
		l.once.Do(func() { close(l.shutDown) })
	}
}

// closingClient is a test email client whose close starts Close and returns
// once Close stopped new email requests. Close waits for the batch, so it
// ends on closed.
func closingClient(t *testing.T, url string) (client Client, close func(), closed <-chan error) {
	logger := &closingLogger{testLogger: testLogger{t.Logf, t.Logf}, shutDown: make(chan struct{})}
	client = newEmailClientFor(t, url, func(config *Config) { config.Logger = logger })
	done := make(chan error, 1)
	close = func() {
		go func() { done <- client.Close() }()
		select {
		case <-logger.shutDown:
		case <-time.After(5 * time.Second):
			t.Errorf("Close didn't log that its lanes shut down")
		}
	}
	return client, close, done
}

func TestEmailsBatchStopsWhenTheClientShutsDown(t *testing.T) {
	t.Run("after a lost answer", func(t *testing.T) {
		var closeClient func()
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			closeClient()
			return stubReply{status: dropConnection}
		})
		client, closeClient, closed := closingClient(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1, "no retry")
		got := result.Results[0]
		require.Equal(t, "unconfirmed", got.Status)
		require.Equal(t, "delivery_unavailable", got.Error.Code)
	})
	t.Run("with a retry due", func(t *testing.T) {
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			return failed(http.StatusServiceUnavailable, "delivery_unavailable")
		})
		client, closeClient, closed := closingClient(t, server.URL)
		senderOf(client).backoff = func(int) time.Duration {
			closeClient()
			return 0
		}

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1, "no retry")
		got := result.Results[0]
		require.Equal(t, "unconfirmed", got.Status)
		require.Equal(t, "delivery_unavailable", got.Error.Code)
	})
}

func TestEmailsBatchSendsNothingOnceTheContextEnds(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(ctx, templateBatch(recipientsTo(2)...))

	require.True(t, errors.Is(err, context.Canceled))
	require.Nil(t, result)
	require.Empty(t, server.received())
}

func TestEmailsBatchRetriesWithTheKeysItMade(t *testing.T) {
	server := newBatchServer(t, func(n int, r batchRequest) stubReply {
		if n == 0 {
			return stubReply{status: dropConnection}
		}
		return queuedAll(n, r)
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(BatchRecipient{To: "a@example.com"}, BatchRecipient{To: "b@example.com"}))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 2)
	require.Equal(t, requests[0].Body, requests[1].Body)
	for i, recipient := range requests[1].recipients() {
		require.Equal(t, recipient["idempotency_key"], result.Results[i].IdempotencyKey)
		require.Equal(t, "queued", result.Results[i].Status)
	}
}

func TestEmailsBatchSendsNothingWithoutAClientThatCanSend(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		client := newDisabledClient(t)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

		require.Nil(t, result)
		require.Equal(t, "client_disabled", requireEmailError(t, err).Code)
	})
	t.Run("off Cloud without an app host", func(t *testing.T) {
		server := newBatchServer(t, queuedAll)
		client := newEmailClientFor(t, server.URL, func(config *Config) { config.AppHost = "" })

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

		require.Nil(t, result)
		require.Equal(t, "missing_app_host", requireEmailError(t, err).Code)
		require.Empty(t, server.received())
	})
}
