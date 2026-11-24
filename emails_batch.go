package posthog

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	emailBatchPath     = "/api/messaging/send/email_batch/"
	maxBatchRecipients = 100
	// minBatchAttemptTimeout outlasts the 30 seconds PostHog waits on its
	// email pipeline for a batch, so the client doesn't give up on an answer
	// that is still coming.
	minBatchAttemptTimeout = 45 * time.Second
)

// EmailBatch is the content of one Emails.Batch call: a TemplateEmailBatch or
// a CodeEmailBatch.
type EmailBatch interface {
	prepareBatch() (*batchRun, *EmailError)
}

// BatchRecipient is one recipient of Emails.Batch. Each gets its own email.
type BatchRecipient struct {
	// To is the recipient's email address.
	To string
	// DistinctID is the recipient's distinct ID in your product.
	DistinctID string
	// IdempotencyKey is 1 to 255 visible ASCII characters, a different one for
	// each recipient of the call. Without it, the client makes one that covers
	// its own retries but not a second call, so pass keys you store when a
	// batch may be cut off and run again: a rerun within 24 hours replays the
	// emails that went out instead of sending them twice. A single send and a
	// batch recipient with the same key and the same email replay each other.
	// Never reuse a key for another email: PostHog replays the earlier email
	// or refuses this one with "idempotency_key_reused", and never sends it.
	IdempotencyKey string
	// TemplateData is only for a TemplateEmailBatch: values for this
	// recipient, merged over the batch's TemplateData. A top-level key set
	// here replaces the same key there.
	TemplateData map[string]interface{}
}

// TemplateEmailBatch sends an email template made transactional in PostHog
// to each recipient.
type TemplateEmailBatch struct {
	// TemplateID is the UUID of the template.
	TemplateID string
	// TemplateData fills the template's {{ variables.* }} placeholders for
	// every recipient.
	TemplateData map[string]interface{}
	// Recipients are 1 to 100 recipients, sent in one request.
	Recipients []BatchRecipient
	// MaxRetries overrides Config.EmailRequestMaxRetries for the batch's
	// request. Ptr(0) turns retries off.
	MaxRetries *int
}

// CodeEmailBatch sends content your code rendered, the same for every
// recipient. Its fields work as CodeEmail's do. A batch takes no Cc, Bcc,
// Attachments or ScheduledAt: send those emails with Send. It takes no
// CaptureEngagement either: use Tracking.
type CodeEmailBatch struct {
	TemplateKey string
	Subject     string
	HTML        string
	Text        string
	From        string
	ReplyTo     []string
	Sensitive   *bool
	Tracking    *EmailTracking
	// Recipients are 1 to 100 recipients, without TemplateData, sent in one
	// request.
	Recipients []BatchRecipient
	// MaxRetries overrides Config.EmailRequestMaxRetries for the batch's
	// request. Ptr(0) turns retries off.
	MaxRetries *int
}

// BatchEmailResult is what happened to one recipient of Emails.Batch. Status
// is one of:
//
//   - "queued": PostHog accepted the email. Read its status with Emails.Get
//     to see whether it went out.
//   - "failed": PostHog didn't queue the email, and Error says why.
//   - "unconfirmed": the client can't tell whether PostHog queued the email,
//     such as after a lost answer, even when PostHog refused the retry, or
//     while another request holds its key. It may still go out. Send it
//     again with the same IdempotencyKey within 24 hours of the first
//     request that used it, so it goes out once. A refused retry of an email
//     whose key belongs to another email is "failed": no attempt could have
//     queued it.
type BatchEmailResult struct {
	// Index is the recipient's position in Recipients.
	Index          int
	To             string
	IdempotencyKey string
	Status         string
	// MessageID is the email's message ID, when PostHog named it.
	MessageID string
	// Replayed is true when an earlier send with the same key had already
	// queued the email, so no new email was sent.
	Replayed bool
	// Error says why the email is not "queued". For an "unconfirmed" email,
	// its Code is "delivery_unavailable", "idempotency_key_in_flight" or
	// "api_error", or PostHog's refusal of a retry after a lost answer, and
	// its Fix says to send it again with the same key.
	Error *EmailError
}

// SendBatchResult is the answer to Emails.Batch.
type SendBatchResult struct {
	// Results hold one result per recipient, in the order of Recipients.
	Results []BatchEmailResult
	// Warnings are the warnings PostHog returned about the batch. They are
	// logged through Config.Logger as well.
	Warnings []EmailWarning
}

type batchRecipientBody struct {
	To             string                 `json:"to"`
	DistinctID     string                 `json:"distinct_id,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key"`
	MessageData    map[string]interface{} `json:"message_data,omitempty"`
}

type templateBatchBody struct {
	TemplateID  string                 `json:"template_id"`
	MessageData map[string]interface{} `json:"message_data"`
	Recipients  []json.RawMessage      `json:"recipients"`
}

type codeBatchBody struct {
	TemplateKey string            `json:"template_key"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	From        string            `json:"from,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Sensitive   *bool             `json:"sensitive,omitempty"`
	Tracking    *EmailTracking    `json:"tracking,omitempty"`
	Recipients  []json.RawMessage `json:"recipients"`
}

func (b TemplateEmailBatch) prepareBatch() (*batchRun, *EmailError) {
	if !isHyphenatedUUID(b.TemplateID) {
		return nil, invalidEmail("invalid_template_id", "TemplateID", "TemplateID must be the template's UUID.")
	}
	messageData := b.TemplateData
	if messageData == nil {
		messageData = map[string]interface{}{}
	}
	run, err := newBatchRun(b.Recipients, true, b.MaxRetries)
	if err != nil {
		return nil, err
	}
	body, encodeErr := encodeEmailBody(templateBatchBody{TemplateID: strings.ToLower(b.TemplateID), MessageData: messageData, Recipients: run.wires()})
	if encodeErr != nil {
		return nil, invalidEmail("invalid_template_data", "TemplateData", "TemplateData must encode as JSON.")
	}
	run.body = body
	return run, nil
}

func (b CodeEmailBatch) prepareBatch() (*batchRun, *EmailError) {
	if err := checkCodeContent("CodeEmailBatch", b.TemplateKey, b.Subject, b.HTML, b.Text, b.ReplyTo); err != nil {
		return nil, err
	}
	run, prepareErr := newBatchRun(b.Recipients, false, b.MaxRetries)
	if prepareErr != nil {
		return nil, prepareErr
	}
	body, err := encodeEmailBody(codeBatchBody{
		TemplateKey: b.TemplateKey,
		Subject:     b.Subject,
		HTML:        b.HTML,
		Text:        b.Text,
		From:        b.From,
		ReplyTo:     b.ReplyTo,
		Sensitive:   b.Sensitive,
		Tracking:    b.Tracking,
		Recipients:  run.wires(),
	})
	if err != nil {
		return nil, newEmailError("unexpected_error", "The client could not encode the email.", err)
	}
	run.body = body
	return run, nil
}

type batchMember struct {
	to, key string
	wire    json.RawMessage
}

// batchRun is one Batch call: its recipients, the request that carries them,
// and what PostHog answered for each.
type batchRun struct {
	members    []batchMember
	body       []byte
	maxRetries *int
	// lost is true once an attempt went out and PostHog may have queued the
	// emails without saying so. Only an answer that queues an email settles
	// it after that.
	lost     bool
	results  []BatchEmailResult
	warnings []EmailWarning
}

func newBatchRun(recipients []BatchRecipient, isTemplate bool, maxRetries *int) (*batchRun, *EmailError) {
	if len(recipients) == 0 {
		return nil, invalidEmail("invalid_recipients", "Recipients", "Recipients must hold at least one recipient.")
	}
	if len(recipients) > maxBatchRecipients {
		return nil, invalidEmail("invalid_recipients", "Recipients", "Recipients holds "+strconv.Itoa(len(recipients))+" recipients, and a batch takes at most 100. Send them in batches of up to 100.")
	}
	if err := checkMaxRetries(maxRetries); err != nil {
		return nil, err
	}
	run := &batchRun{maxRetries: maxRetries, results: make([]BatchEmailResult, len(recipients)), warnings: []EmailWarning{}}
	firstWithKey := map[string]int{}
	for i, recipient := range recipients {
		member, err := newBatchMember(i, recipient, isTemplate)
		if err != nil {
			return nil, err
		}
		if first, seen := firstWithKey[member.key]; seen {
			where := "Recipients[" + strconv.Itoa(i) + "]"
			return nil, invalidEmail("invalid_idempotency_key", where+".IdempotencyKey", where+" has the same IdempotencyKey as Recipients["+strconv.Itoa(first)+"]. Give each recipient its own key.")
		}
		firstWithKey[member.key] = i
		run.members = append(run.members, member)
	}
	return run, nil
}

func (r *batchRun) wires() []json.RawMessage {
	wires := make([]json.RawMessage, len(r.members))
	for i, member := range r.members {
		wires[i] = member.wire
	}
	return wires
}

func newBatchMember(i int, recipient BatchRecipient, isTemplate bool) (batchMember, *EmailError) {
	where := "Recipients[" + strconv.Itoa(i) + "]"
	atPosition := func(err *EmailError) (batchMember, *EmailError) {
		err.Field = where + "." + err.Field
		err.Message = where + ": " + err.Message
		return batchMember{}, err
	}
	if err := checkRecipient(recipient.To); err != nil {
		return atPosition(err)
	}
	key, err := idempotencyKeyOrNew(recipient.IdempotencyKey)
	if err != nil {
		return atPosition(err)
	}
	if recipient.TemplateData != nil && !isTemplate {
		return batchMember{}, invalidEmail("invalid_form", where+".TemplateData", where+" has TemplateData, but a CodeEmailBatch sends the same content to every recipient. Send personalized content with a TemplateEmailBatch or with Send.")
	}
	wire, encodeErr := encodeEmailBody(batchRecipientBody{To: recipient.To, DistinctID: recipient.DistinctID, IdempotencyKey: key, MessageData: recipient.TemplateData})
	if encodeErr != nil {
		return atPosition(invalidEmail("invalid_template_data", "TemplateData", "TemplateData must encode as JSON."))
	}
	return batchMember{to: recipient.To, key: key, wire: wire}, nil
}

func (r *batchRun) set(i int, status string, result BatchEmailResult) {
	if status == "unconfirmed" && result.Error != nil && result.Error.Fix == "" {
		withFix := *result.Error
		withFix.Fix = resendWithTheSameKey
		result.Error = &withFix
	}
	result.Index, result.To, result.IdempotencyKey, result.Status = i, r.members[i].to, r.members[i].key, status
	r.results[i] = result
}

// failAll settles every recipient after the request failed as a whole. They
// stay "unconfirmed" when an attempt that went out may have queued them;
// sent is the last of those attempts.
func (r *batchRun) failAll(err, sent *EmailError) {
	reason, status := err, "failed"
	if r.lost {
		status, reason = "unconfirmed", unconfirmedEmail("")
		if keysInFlight(sent) {
			reason = sent
		}
	}
	for i := range r.members {
		r.set(i, status, BatchEmailResult{Error: reason})
	}
}

func (r *batchRun) result() *SendBatchResult {
	return &SendBatchResult{Results: r.results, Warnings: r.warnings}
}

// answer settles each recipient from PostHog's answer, or returns why the
// request failed as a whole.
func (r *batchRun) answer(reply emailReply) *EmailError {
	if !reply.accepted() {
		return batchReplyError(reply, len(r.members))
	}
	slots, _ := reply.fields["results"].([]interface{})
	if len(slots) != len(r.members) {
		return reply.apiError("PostHog answered the batch without a result for each recipient.")
	}
	for i := range r.members {
		fields, _ := slots[i].(map[string]interface{})
		r.settle(i, emailReply{fields: fields})
	}
	r.warnings = append(r.warnings, reply.warnings()...)
	return nil
}

func (r *batchRun) settle(i int, slot emailReply) {
	messageID := slot.text("message_id")
	if slot.fields["error"] == nil && slot.text("status") == "queued" && messageID != "" {
		replayed, _ := slot.fields["replayed"].(bool)
		r.set(i, "queued", BatchEmailResult{MessageID: messageID, Replayed: replayed})
		return
	}
	answer := slotError(slot.fields["error"], messageID)
	// A reused key belongs to another email, so no attempt of this one
	// could have queued it.
	if r.lost && answer.Code != "idempotency_key_reused" {
		kept := *answer
		kept.Fix = resendWithTheSameKey
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: &kept})
		return
	}
	switch answer.Code {
	case "delivery_unavailable", "idempotency_key_in_flight", "api_error":
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: answer})
	default:
		r.set(i, "failed", BatchEmailResult{MessageID: messageID, Error: answer})
	}
}

// keysInFlight is PostHog's 409 for keys an earlier request still holds.
func keysInFlight(err *EmailError) bool {
	return err.Code == "idempotency_key_in_flight" && err.StatusCode == http.StatusConflict
}

// mayHaveQueued is an attempt's error after which PostHog may still have
// queued the emails: no answer, a cut-off answer, a 5xx, a 2xx the client
// couldn't read, or keys an earlier request still holds.
func mayHaveQueued(err *EmailError) bool {
	return err.Code == "network_error" || keysInFlight(err) || err.StatusCode >= http.StatusInternalServerError || (err.StatusCode >= 200 && err.StatusCode < 300)
}

func (s *emailSender) Batch(ctx context.Context, batch EmailBatch) (*SendBatchResult, error) {
	if isNilEmail(batch) {
		return nil, newEmailError("invalid_form", "Batch needs a TemplateEmailBatch or a CodeEmailBatch.", nil)
	}
	run, err := batch.prepareBatch()
	if err != nil {
		return nil, err
	}
	finish, err := s.start(emailNotSent, "Sending email needs Config.SecretKey, a project secret API key with the transactional_email:write scope.")
	if err != nil {
		return nil, err
	}
	defer finish()

	maxRetries := s.maxRetries
	if run.maxRetries != nil {
		maxRetries = *run.maxRetries
	}
	if ctx.Err() != nil {
		return nil, newEmailError("network_error", "The call's context ended, so the batch wasn't sent.", ctx.Err())
	}
	header := s.header()
	header.Set("Content-Type", "application/json")
	attempts := 0
	// sent is the outcome of the last attempt that went out. A retry the
	// client stops before sending it doesn't change what PostHog may have
	// queued.
	var sent *EmailError
	err = s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
		if attempts++; attempts > 1 && s.isClosed() {
			return newEmailError("client_closed", "The PostHog client is shut down, so it didn't retry the batch.", ErrClosed)
		}
		if attempts > 1 && ctx.Err() != nil {
			// The retry wait and the context can end together. A retry that
			// never went out leaves PostHog's last answer standing.
			return sent.interruptedBy(ctx.Err())
		}
		reply, err := s.doFor(ctx, s.batchAttemptTimeout(), http.MethodPost, s.appHost+emailBatchPath, header, run.body)
		if err == nil {
			err = run.answer(reply)
		}
		if err != nil && mayHaveQueued(err) {
			run.lost = true
		}
		sent = err
		return err
	})
	if err != nil {
		run.failAll(err, sent)
	}
	s.logWarnings(run.warnings)
	if err != nil {
		return run.result(), err
	}
	return run.result(), nil
}

func (s *emailSender) batchAttemptTimeout() time.Duration {
	if s.timeout > minBatchAttemptTimeout {
		return s.timeout
	}
	return minBatchAttemptTimeout
}

func (s *emailSender) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *emailSender) logWarnings(warnings []EmailWarning) {
	for _, warning := range warnings {
		s.logger.Warnf("PostHog email warning %s: %s", warning.Code, warning.Message)
	}
}

// batchReplyError is a batch request that failed as a whole.
func batchReplyError(reply emailReply, recipients int) *EmailError {
	err := reply.error()
	if reply.status == http.StatusRequestEntityTooLarge && err.Fix == "" {
		err.Fix = "Split the recipients into smaller batches."
		if recipients == 1 {
			err.Fix = "Make this recipient's email smaller."
		}
	}
	if reply.status == http.StatusNotFound && reply.text("code") == "" {
		err.Code, err.Name = "not_found", EmailErrorNotFound
		err.Message = "PostHog found no batch send for this request, so no email was sent."
		err.Why = "The template doesn't exist, or batch sends are off for this project or this PostHog version."
	}
	if err.Code == "not_found" && err.Fix == "" {
		err.Fix = "Check TemplateID, or send each email the batch didn't queue with Emails.Send and its recipient's IdempotencyKey."
	}
	if err.Code == "batch_unavailable" {
		err.Fix = "Until batch sends are on for the project, send each email the batch didn't queue with Emails.Send and its recipient's IdempotencyKey."
	}
	return err
}

func slotError(raw interface{}, messageID string) *EmailError {
	fields, ok := raw.(map[string]interface{})
	if !ok {
		return &EmailError{Name: EmailErrorApplication, Code: "api_error", Message: "PostHog answered without a readable result for this email, so it may still go out.", MessageID: messageID}
	}
	slot := emailReply{fields: fields}
	code := slot.text("code")
	if code == "" {
		code = "api_error"
	}
	status := 0
	if number, ok := fields["status"].(float64); ok && number == float64(int(number)) {
		status = int(number)
	}
	message := slot.text("detail")
	if message == "" {
		message = "PostHog didn't queue this email: " + code + "."
	}
	return &EmailError{Name: emailErrorName(status, code), Code: code, Message: message, StatusCode: status, MessageID: messageID}
}

const resendWithTheSameKey = "Send it again with the same IdempotencyKey within 24 hours of the first request that used it, so it goes out once."

func unconfirmedEmail(messageID string) *EmailError {
	return &EmailError{Name: EmailErrorServiceUnavailable, Code: "delivery_unavailable", Message: "PostHog didn't confirm this email, so it may still go out.", Fix: resendWithTheSameKey, MessageID: messageID}
}
