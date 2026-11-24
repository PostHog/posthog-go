package posthog

import (
	"bytes"
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

// batchServer answers each batch send and status read with answer, which
// sees the requests before it, and records what it received.
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
		if route != "POST /api/messaging/send/email_batch/" && route != "GET /api/messaging/emails/status/" {
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

func TestEmailsBatchSplitsBy100RecipientsInOrder(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(250)...))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 3)
	var sent []string
	for i, size := range []int{100, 100, 50} {
		require.Len(t, requests[i].recipients(), size)
		for _, recipient := range requests[i].recipients() {
			sent = append(sent, recipient["to"].(string))
		}
	}
	require.Len(t, result.Results, 250)
	for i, r := range result.Results {
		require.Equal(t, i, r.Index)
		require.Equal(t, fmt.Sprintf("user%d@example.com", i), sent[i])
		require.Equal(t, fmt.Sprintf("user%d@example.com", i), r.To)
		require.Equal(t, "queued", r.Status)
		require.Equal(t, messageIDFor(fmt.Sprintf("digest:%d", i)), r.MessageID)
	}
}

// batchBodyCap is the largest request body the client sends, 4 MiB.
const batchBodyCap = 4 << 20

// emptyTemplateBatchBody is the length of a template batch request without
// TemplateData or recipients.
var emptyTemplateBatchBody = len(`{"template_id":"` + testTemplateID + `","message_data":{},"recipients":[]}`)

// paddedRecipient carries data that makes its JSON in the request body size
// bytes long.
func paddedRecipient(t *testing.T, index, size int) BatchRecipient {
	to, key := fmt.Sprintf("user%d@example.com", index), fmt.Sprintf("digest:%d", index)
	unpadded, err := json.Marshal(map[string]interface{}{"to": to, "idempotency_key": key, "message_data": map[string]interface{}{"pad": ""}})
	require.NoError(t, err)
	return BatchRecipient{To: to, IdempotencyKey: key, TemplateData: map[string]interface{}{"pad": strings.Repeat("x", size-len(unpadded))}}
}

// fillingRecipients are three recipients from first whose JSON, with the
// commas between them, is room bytes long.
func fillingRecipients(t *testing.T, first, room int) []BatchRecipient {
	third := (room - 2) / 3
	return []BatchRecipient{
		paddedRecipient(t, first, third),
		paddedRecipient(t, first+1, third),
		paddedRecipient(t, first+2, room-2-2*third),
	}
}

func TestEmailsBatchSplitsByTheBodySize(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)
	recipients := append(fillingRecipients(t, 0, batchBodyCap-emptyTemplateBatchBody),
		paddedRecipient(t, 3, batchBodyCap),
		paddedRecipient(t, 4, batchBodyCap/3),
		paddedRecipient(t, 5, 100),
	)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipients...))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 2)
	require.Len(t, requests[0].recipients(), 3)
	require.Equal(t, batchBodyCap, requests[0].Size, "the first request fills the body exactly")
	require.Len(t, requests[1].recipients(), 2)
	require.Equal(t, []string{"queued", "queued", "queued", "failed", "queued", "queued"}, statuses(result))
	tooLarge := result.Results[3].Error
	require.Equal(t, "content_too_large", tooLarge.Code)
	require.Equal(t, EmailErrorValidation, tooLarge.Name)
	require.Equal(t, "Recipients", tooLarge.Field)
	require.Equal(t, "Send this recipient's email with Emails.Send.", tooLarge.Fix)
}

func TestEmailsBatchSplitsABodyOneByteOverTheCap(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	client := newEmailClientFor(t, server.URL)

	_, err := client.Emails().Batch(context.Background(), templateBatch(fillingRecipients(t, 0, batchBodyCap-emptyTemplateBatchBody+1)...))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 2)
	require.Len(t, requests[0].recipients(), 2)
	require.LessOrEqual(t, requests[0].Size, batchBodyCap)
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
		name       string
		answers    []func(r batchRequest) stubReply
		status     string
		errorCode  string
		laterError string
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
			status:     "unconfirmed",
			errorCode:  "delivery_unavailable",
			laterError: "recipient_suppressed",
		},
		{
			name: "a refusal after a 503",
			answers: []func(batchRequest) stubReply{
				func(batchRequest) stubReply { return failed(http.StatusServiceUnavailable, "delivery_unavailable") },
				func(r batchRequest) stubReply { return slots(r, refused, "") },
			},
			status:     "unconfirmed",
			errorCode:  "delivery_unavailable",
			laterError: "recipient_suppressed",
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
			if tt.laterError == "" {
				require.Nil(t, got.LaterError)
			} else {
				require.Equal(t, tt.laterError, got.LaterError.Code)
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

func TestEmailsBatchStopsAtARequestThatFailsAsAWhole(t *testing.T) {
	server := newBatchServer(t, func(n int, r batchRequest) stubReply {
		if n == 1 {
			return stubReply{status: http.StatusTooManyRequests, body: `{"code":"quota_limited","detail":"Over quota."}`}
		}
		return queuedAll(n, r)
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(250)...))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "quota_limited", emailErr.Code)
	require.Len(t, server.received(), 2)
	require.Len(t, result.Results, 250)
	for i, r := range result.Results {
		switch {
		case i < 100:
			require.Equal(t, "queued", r.Status)
		case i < 200:
			require.Equal(t, "failed", r.Status)
			require.Equal(t, emailErr, r.Error)
		default:
			require.Equal(t, "not_attempted", r.Status)
			require.Equal(t, "not_attempted", r.Error.Code)
		}
	}
}

func TestEmailsBatchKeepsALostEmailUnconfirmedWhenTheRetryIsRefused(t *testing.T) {
	server := newBatchServer(t, func(n int, r batchRequest) stubReply {
		switch {
		case r.Method == http.MethodGet:
			return stubReply{status: http.StatusNotFound}
		case n == 0:
			return stubReply{status: dropConnection}
		default:
			return stubReply{status: http.StatusTooManyRequests, body: `{"code":"quota_limited","detail":"Over quota."}`}
		}
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(1)...))

	require.Equal(t, "quota_limited", requireEmailError(t, err).Code)
	got := result.Results[0]
	require.Equal(t, "unconfirmed", got.Status)
	require.Equal(t, "delivery_unavailable", got.Error.Code)
	require.Equal(t, "quota_limited", got.LaterError.Code)
}

func TestEmailsBatchReadsLostEmailsByTheirKeys(t *testing.T) {
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		if r.Method == http.MethodPost {
			return stubReply{status: dropConnection}
		}
		switch r.Query.Get("idempotency_key") {
		case "digest:0":
			return stubReply{status: http.StatusOK, body: `{"id":"m-0","status":"delivered","created_at":"2026-11-24T05:00:00Z","status_at":"2026-11-24T05:00:01Z"}`}
		case "digest:2":
			return stubReply{status: http.StatusOK, body: `{"id":"m-2","status":"failed","created_at":"2026-11-24T05:00:00Z","status_at":"2026-11-24T05:00:00Z","reason":"recipient_suppressed"}`}
		case "digest:3":
			return stubReply{status: http.StatusOK, body: `{"id":"m-3","status":"failed","created_at":"2026-11-24T05:00:00Z","status_at":"2026-11-24T05:00:02Z","reason":"provider_error"}`}
		}
		return stubReply{status: http.StatusNotFound, body: `{"code":"not_found","detail":"Not found."}`}
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(1) })

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(4)...))

	require.Equal(t, "network_error", requireEmailError(t, err).Code)
	requests := server.received()
	require.Len(t, requests, 6)
	for n, read := range requests[2:] {
		require.Equal(t, http.MethodGet, read.Method)
		require.Equal(t, fmt.Sprintf("digest:%d", n), read.Query.Get("idempotency_key"))
	}
	for i, messageID := range []string{"m-0", "", "m-2", "m-3"} {
		got := result.Results[i]
		require.Equal(t, "unconfirmed", got.Status, "a read by key gives the message ID and leaves the status")
		require.Equal(t, messageID, got.MessageID)
		require.Equal(t, "delivery_unavailable", got.Error.Code)
	}
}

func TestEmailsBatchKeepsRecipientsUnconfirmedWhileTheirKeysAreInFlight(t *testing.T) {
	server := newBatchServer(t, func(n int, r batchRequest) stubReply {
		switch {
		case r.Method == http.MethodGet:
			return stubReply{status: http.StatusNotFound}
		case n == 0:
			return failed(http.StatusConflict, "idempotency_key_in_flight")
		default:
			return stubReply{status: http.StatusTooManyRequests, body: `{"code":"quota_limited","detail":"Over quota."}`}
		}
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(1) })

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	require.Equal(t, "quota_limited", requireEmailError(t, err).Code)
	require.Equal(t, []string{"unconfirmed", "unconfirmed"}, statuses(result), "an earlier request with these keys may still queue them")
	for _, r := range result.Results {
		require.Equal(t, "quota_limited", r.LaterError.Code)
	}
	require.Len(t, server.received(), 4, "two sends, then a read of each email by its key")
}

func TestEmailsBatchReadsUnconfirmedEmailsWhenItEnds(t *testing.T) {
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		if r.Method == http.MethodGet {
			return stubReply{status: http.StatusOK, body: `{"id":"m-1","status":"queued","created_at":"2026-11-24T05:00:00Z","status_at":"2026-11-24T05:00:00Z"}`}
		}
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
	requests := server.received()
	require.Len(t, requests, 2)
	require.Equal(t, "digest:1", requests[1].Query.Get("idempotency_key"))
	require.Equal(t, "queued", result.Results[0].Status)
	require.Equal(t, "unconfirmed", result.Results[1].Status)
	require.Equal(t, "m-1", result.Results[1].MessageID)
	require.Equal(t, "delivery_unavailable", result.Results[1].Error.Code)
}

func TestEmailsBatchStopsReadingWhenPostHogStaysUnreachable(t *testing.T) {
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		if r.Method == http.MethodPost {
			return failed(http.StatusServiceUnavailable, "delivery_unavailable")
		}
		return failed(http.StatusServiceUnavailable, "service_unavailable")
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(0) })

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(3)...))

	require.Equal(t, "delivery_unavailable", requireEmailError(t, err).Code)
	require.Len(t, server.received(), 2, "one send and one read, without retries")
	require.Equal(t, []string{"unconfirmed", "unconfirmed", "unconfirmed"}, statuses(result))
}

func TestEmailsBatchEncodesAKeyInTheStatusRead(t *testing.T) {
	key := "order:1&x=2+3#4%"
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		if r.Method == http.MethodPost {
			return stubReply{status: dropConnection}
		}
		return stubReply{status: http.StatusNotFound}
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.EmailRequestMaxRetries = Ptr(0) })

	_, err := client.Emails().Batch(context.Background(), templateBatch(BatchRecipient{To: "a@example.com", IdempotencyKey: key}))

	require.Error(t, err)
	requests := server.received()
	require.Len(t, requests, 2)
	require.Equal(t, url.Values{"idempotency_key": {key}}, requests[1].Query)
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
	server := newBatchServer(t, func(_ int, r batchRequest) stubReply {
		if r.Method == http.MethodGet {
			return stubReply{status: http.StatusNotFound}
		}
		return stubReply{status: http.StatusOK, body: `{"results":[{"message_id":"m-1","status":"queued"}]}`}
	})
	client := newEmailClientFor(t, server.URL)

	result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "api_error", emailErr.Code)
	require.Equal(t, http.StatusOK, emailErr.StatusCode)
	requests := server.received()
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Len(t, requests, 3, "no retry, then a read of each email by its key")
	require.Equal(t, []string{"unconfirmed", "unconfirmed"}, statuses(result))
	for _, r := range result.Results {
		require.Equal(t, "delivery_unavailable", r.Error.Code)
		require.Nil(t, r.LaterError, "no later attempt answered")
	}
}

func TestEmailsBatchReturnsWarningsOnce(t *testing.T) {
	warning := `,"warnings":[{"code":"sandbox_sender","message":"Sent from the PostHog test sender."}]`
	for _, laterFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("a later request fails: %t", laterFails), func(t *testing.T) {
			server := newBatchServer(t, func(n int, r batchRequest) stubReply {
				if laterFails && n == 2 {
					return failed(http.StatusBadRequest, "invalid_request")
				}
				return slots(r, queuedSlot, warning)
			})
			logs := &recordingLogger{}
			client := newEmailClientFor(t, server.URL, func(config *Config) { config.Logger = logs })

			result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(250)...))

			if laterFails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []EmailWarning{{Code: "sandbox_sender", Message: "Sent from the PostHog test sender."}}, result.Warnings)
			require.Equal(t, []string{"PostHog email warning sandbox_sender: Sent from the PostHog test sender."}, logs.warnings())
		})
	}
}

func TestEmailsBatchNamesTheCallersPositionInARefusedField(t *testing.T) {
	tests := []struct{ attr, want string }{
		{attr: "recipients__37__to", want: "recipients__137__to"},
		{attr: "recipients__0", want: "recipients__100"},
		{attr: "recipients__99999999999999999999__to", want: "recipients"},
		{attr: "recipients__50__to", want: "recipients"},
		{attr: "template_id", want: "template_id"},
	}
	for _, tt := range tests {
		t.Run(tt.attr, func(t *testing.T) {
			server := newBatchServer(t, func(n int, r batchRequest) stubReply {
				if n == 1 {
					return stubReply{status: http.StatusBadRequest, body: `{"code":"invalid_input","detail":"Enter a valid email address.","attr":"` + tt.attr + `"}`}
				}
				return queuedAll(n, r)
			})
			client := newEmailClientFor(t, server.URL)

			_, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(150)...))

			require.Equal(t, tt.want, requireEmailError(t, err).Field)
		})
	}
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
	t.Run("between requests", func(t *testing.T) {
		var closeClient func()
		server := newBatchServer(t, func(n int, r batchRequest) stubReply {
			closeClient()
			return queuedAll(n, r)
		})
		client, closeClient, closed := closingClient(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(150)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1)
		require.Equal(t, "queued", result.Results[99].Status)
		require.Equal(t, "not_attempted", result.Results[100].Status)
		require.Equal(t, "client_closed", result.Results[100].Error.Code)
	})
	t.Run("after a lost answer", func(t *testing.T) {
		var closeClient func()
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			closeClient()
			return stubReply{status: dropConnection}
		})
		client, closeClient, closed := closingClient(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(150)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1, "no retry and no read by key")
		got := result.Results[0]
		require.Equal(t, "unconfirmed", got.Status)
		require.Equal(t, "delivery_unavailable", got.Error.Code)
		require.Nil(t, got.LaterError, "no later attempt reached PostHog")
		require.Equal(t, "not_attempted", result.Results[100].Status)
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

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(150)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1, "no retry and no read by key")
		got := result.Results[0]
		require.Equal(t, "unconfirmed", got.Status)
		require.Equal(t, "delivery_unavailable", got.Error.Code)
		require.Nil(t, got.LaterError, "no later attempt reached PostHog")
		require.Equal(t, "not_attempted", result.Results[100].Status)
	})
	t.Run("before the halves of a request too large for the pipeline", func(t *testing.T) {
		var closeClient func()
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			closeClient()
			return stubReply{status: http.StatusRequestEntityTooLarge, body: `{"code":"content_too_large","detail":"The batch is larger than PostHog can send at once, so PostHog didn't send it."}`}
		})
		client, closeClient, closed := closingClient(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(4)...))

		require.NoError(t, <-closed)
		require.Equal(t, "client_closed", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1, "neither half is sent")
		require.Equal(t, []string{"not_attempted", "not_attempted", "not_attempted", "not_attempted"}, statuses(result))
	})
}

func TestEmailsBatchSendsNothingMoreOnceTheContextEnds(t *testing.T) {
	server := newBatchServer(t, queuedAll)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterAnswer := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		res, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(body))
		cancel()
		return res, err
	})
	client := newEmailClientFor(t, server.URL, func(config *Config) { config.Transport = cancelAfterAnswer })

	result, err := client.Emails().Batch(ctx, templateBatch(recipientsTo(150)...))

	require.True(t, errors.Is(err, context.Canceled))
	require.Len(t, server.received(), 1)
	require.Equal(t, "queued", result.Results[99].Status)
	require.Equal(t, "not_attempted", result.Results[100].Status)
}

func TestEmailsBatchHalvesARequestTooLargeForThePipeline(t *testing.T) {
	tooLarge := stubReply{status: http.StatusRequestEntityTooLarge, body: `{"code":"content_too_large","detail":"The batch is larger than PostHog can send at once, so PostHog didn't send it."}`}
	t.Run("until each request fits", func(t *testing.T) {
		server := newBatchServer(t, func(n int, r batchRequest) stubReply {
			recipients := r.recipients()
			if len(recipients) > 2 {
				return tooLarge
			}
			for _, recipient := range recipients {
				if recipient["to"] == "user3@example.com" {
					return tooLarge
				}
			}
			return queuedAll(n, r)
		})
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(5)...))

		require.NoError(t, err)
		var sent [][]string
		for _, request := range server.received() {
			var to []string
			for _, recipient := range request.recipients() {
				to = append(to, strings.TrimSuffix(recipient["to"].(string), "@example.com"))
			}
			sent = append(sent, to)
		}
		require.Equal(t, [][]string{{"user0", "user1", "user2", "user3", "user4"}, {"user0", "user1"}, {"user2", "user3", "user4"}, {"user2"}, {"user3", "user4"}, {"user3"}, {"user4"}}, sent)
		require.Equal(t, []string{"queued", "queued", "queued", "failed", "queued"}, statuses(result))
		require.Equal(t, "content_too_large", result.Results[3].Error.Code)
	})
	t.Run("not when every recipient shares the content that is too large", func(t *testing.T) {
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			return stubReply{status: http.StatusBadRequest, body: `{"code":"content_too_large","detail":"The batch's shared content is too large."}`}
		})
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(4)...))

		require.Equal(t, "content_too_large", requireEmailError(t, err).Code)
		require.Len(t, server.received(), 1)
		require.Equal(t, []string{"failed", "failed", "failed", "failed"}, statuses(result))
	})
	t.Run("not for a 413 from a proxy", func(t *testing.T) {
		server := newBatchServer(t, func(int, batchRequest) stubReply {
			return stubReply{status: http.StatusRequestEntityTooLarge, body: `<html>413 Request Entity Too Large</html>`}
		})
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(150)...))

		require.Equal(t, http.StatusRequestEntityTooLarge, requireEmailError(t, err).StatusCode)
		require.Len(t, server.received(), 1)
		require.Equal(t, "failed", result.Results[0].Status)
		require.Equal(t, "not_attempted", result.Results[100].Status)
	})
	t.Run("not after a lost answer", func(t *testing.T) {
		server := newBatchServer(t, func(n int, r batchRequest) stubReply {
			switch {
			case r.Method == http.MethodGet:
				return stubReply{status: http.StatusNotFound}
			case n == 0:
				return stubReply{status: dropConnection}
			}
			return tooLarge
		})
		client := newEmailClientFor(t, server.URL)

		result, err := client.Emails().Batch(context.Background(), templateBatch(recipientsTo(2)...))

		require.Equal(t, "content_too_large", requireEmailError(t, err).Code)
		require.Equal(t, []string{"unconfirmed", "unconfirmed"}, statuses(result))
		require.Equal(t, "content_too_large", result.Results[0].LaterError.Code)
	})
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
