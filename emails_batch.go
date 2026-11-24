package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	emailBatchPath     = "/api/messaging/send/email_batch/"
	maxBatchRecipients = 100
	maxBatchBodyBytes  = 4 << 20
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
	// Recipients are 1 or more recipients. The client sends at most 100, and
	// at most 4 MiB of JSON, per request.
	Recipients []BatchRecipient
	// MaxRetries overrides Config.EmailRequestMaxRetries for each request of
	// the batch. Ptr(0) turns retries off.
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
	// Recipients are 1 or more recipients, without TemplateData. The client
	// sends at most 100, and at most 4 MiB of JSON, per request.
	Recipients []BatchRecipient
	// MaxRetries overrides Config.EmailRequestMaxRetries for each request of
	// the batch. Ptr(0) turns retries off.
	MaxRetries *int
}

// BatchEmailResult is what happened to one recipient of Emails.Batch. Status
// is one of:
//
//   - "queued": PostHog accepted the email. Read its status with Emails.Get
//     to see whether it went out.
//   - "failed": PostHog didn't queue the email, and Error says why.
//   - "unconfirmed": the client can't tell whether PostHog queued the email,
//     such as after a lost answer. It may still go out. Send it again with
//     the same IdempotencyKey within 24 hours, so it goes out once.
//     LaterError holds what a retry in the same call answered, which
//     describes only that retry.
//   - "not_attempted": an earlier request of the batch failed, so the client
//     sent nothing for this recipient.
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
	// "api_error".
	Error *EmailError
	// LaterError is, for an "unconfirmed" email, a later answer about it.
	LaterError *EmailError
}

// SendBatchResult is the answer to Emails.Batch.
type SendBatchResult struct {
	// Results hold one result per recipient, in the order of Recipients.
	Results []BatchEmailResult
	// Warnings are the warnings PostHog returned about the batch, each once.
	// They are logged through Config.Logger as well.
	Warnings []EmailWarning
}

type batchRecipientBody struct {
	To             string                 `json:"to"`
	DistinctID     string                 `json:"distinct_id,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key"`
	MessageData    map[string]interface{} `json:"message_data,omitempty"`
}

// The recipients field comes last, since the client splices each request's
// recipients into the encoded shared email.
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
	empty, err := encodeEmailBody(templateBatchBody{TemplateID: strings.ToLower(b.TemplateID), MessageData: messageData, Recipients: []json.RawMessage{}})
	if err != nil {
		return nil, invalidEmail("invalid_template_data", "TemplateData", "TemplateData must encode as JSON.")
	}
	return newBatchRun(empty, b.Recipients, true, b.MaxRetries)
}

func (b CodeEmailBatch) prepareBatch() (*batchRun, *EmailError) {
	if err := checkCodeContent("CodeEmailBatch", b.TemplateKey, b.Subject, b.HTML, b.Text, b.ReplyTo); err != nil {
		return nil, err
	}
	empty, err := encodeEmailBody(codeBatchBody{
		TemplateKey: b.TemplateKey,
		Subject:     b.Subject,
		HTML:        b.HTML,
		Text:        b.Text,
		From:        b.From,
		ReplyTo:     b.ReplyTo,
		Sensitive:   b.Sensitive,
		Tracking:    b.Tracking,
		Recipients:  []json.RawMessage{},
	})
	if err != nil {
		return nil, newEmailError("unexpected_error", "The client could not encode the email.", err)
	}
	return newBatchRun(empty, b.Recipients, false, b.MaxRetries)
}

type batchMember struct {
	to, key string
	wire    []byte
}

// batchRun is one Batch call: its recipients split into requests, and what
// PostHog answered for each recipient so far. A recipient whose result reads
// "unconfirmed" may have been queued, and keeps that status until an answer
// queues it.
type batchRun struct {
	members    []batchMember
	empty      []byte
	chunks     [][]int
	maxRetries *int
	results    []BatchEmailResult
	warnings   []EmailWarning
}

func newBatchRun(empty []byte, recipients []BatchRecipient, isTemplate bool, maxRetries *int) (*batchRun, *EmailError) {
	if len(recipients) == 0 {
		return nil, invalidEmail("invalid_recipients", "Recipients", "Recipients must hold at least one recipient.")
	}
	if err := checkMaxRetries(maxRetries); err != nil {
		return nil, err
	}
	run := &batchRun{empty: empty, maxRetries: maxRetries, results: make([]BatchEmailResult, len(recipients)), warnings: []EmailWarning{}}
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
	run.split()
	return run, nil
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

// split packs the recipients, in order, into requests of at most 100
// recipients and maxBatchBodyBytes. A recipient too large for any request
// fails alone.
func (r *batchRun) split() {
	var current []int
	size := len(r.empty)
	for i, member := range r.members {
		if len(r.empty)+len(member.wire) > maxBatchBodyBytes {
			r.set(i, "failed", BatchEmailResult{Error: recipientTooLarge()})
			continue
		}
		added := len(member.wire)
		if len(current) > 0 {
			added++
		}
		if len(current) > 0 && (len(current) == maxBatchRecipients || size+added > maxBatchBodyBytes) {
			r.chunks = append(r.chunks, current)
			current, size, added = nil, len(r.empty), len(member.wire)
		}
		current = append(current, i)
		size += added
	}
	if len(current) > 0 {
		r.chunks = append(r.chunks, current)
	}
}

func (r *batchRun) body(chunk []int) []byte {
	wires := make([][]byte, len(chunk))
	for n, i := range chunk {
		wires[n] = r.members[i].wire
	}
	return append(append(append([]byte{}, r.empty[:len(r.empty)-len("]}")]...), bytes.Join(wires, []byte(","))...), "]}"...)
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

func (r *batchRun) unconfirmed(i int) bool { return r.results[i].Status == "unconfirmed" }

func (r *batchRun) lost(chunk []int) {
	for _, i := range chunk {
		messageID := r.results[i].MessageID
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: unconfirmedEmail(messageID)})
	}
}

// answer settles each recipient of the chunk from PostHog's answer, or
// returns why the request failed as a whole.
func (r *batchRun) answer(chunk []int, reply emailReply) *EmailError {
	if !reply.accepted() {
		return batchReplyError(reply, chunk)
	}
	slots, _ := reply.fields["results"].([]interface{})
	if len(slots) != len(chunk) {
		return reply.apiError("PostHog answered the batch without a result for each recipient.")
	}
	for n, i := range chunk {
		fields, _ := slots[n].(map[string]interface{})
		r.settle(i, emailReply{fields: fields})
	}
	for _, warning := range reply.warnings() {
		if !slices.Contains(r.warnings, warning) {
			r.warnings = append(r.warnings, warning)
		}
	}
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
	switch {
	case r.unconfirmed(i):
		earlier := r.results[i]
		if messageID == "" {
			messageID = earlier.MessageID
		}
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: earlier.Error, LaterError: answer})
	case answer.Code == "delivery_unavailable" || answer.Code == "idempotency_key_in_flight" || answer.Code == "api_error":
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: answer})
	default:
		r.set(i, "failed", BatchEmailResult{MessageID: messageID, Error: answer})
	}
}

// stop records that chunk failed as a whole with err, so the batch sends none
// of the pending requests. A recipient an earlier attempt may have queued
// stays "unconfirmed", with err as its LaterError when PostHog answered a
// later attempt with it.
func (r *batchRun) stop(chunk []int, pending [][]int, err *EmailError, mayHaveQueued bool) {
	for _, i := range chunk {
		switch {
		case mayHaveQueued:
		case r.unconfirmed(i):
			earlier := r.results[i]
			later := err
			if err.StatusCode == 0 {
				later = nil
			}
			r.set(i, "unconfirmed", BatchEmailResult{MessageID: earlier.MessageID, Error: earlier.Error, LaterError: later})
		default:
			r.set(i, "failed", BatchEmailResult{Error: err})
		}
	}
	r.skip(pending, &EmailError{Name: EmailErrorApplication, Code: "not_attempted", Message: "An earlier request of the batch failed, so the client sent nothing for this recipient."})
}

func (r *batchRun) skip(pending [][]int, err *EmailError) {
	for _, chunk := range pending {
		for _, i := range chunk {
			r.set(i, "not_attempted", BatchEmailResult{Error: err})
		}
	}
}

func (r *batchRun) result() *SendBatchResult {
	return &SendBatchResult{Results: r.results, Warnings: r.warnings}
}

// mayHaveQueued is an attempt's error after which PostHog may still have
// queued the emails: no answer, a cut-off answer, a 5xx, a 2xx the client
// couldn't read, or keys an earlier request still holds.
func mayHaveQueued(err *EmailError) bool {
	return err.Code == "network_error" || err.Code == "idempotency_key_in_flight" || err.StatusCode >= http.StatusInternalServerError || (err.StatusCode >= 200 && err.StatusCode < 300)
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
	header := s.header()
	header.Set("Content-Type", "application/json")
	// pending is a stack, so the next request is last and a split request's
	// halves go out next, in order.
	pending := slices.Clone(run.chunks)
	slices.Reverse(pending)
	for sent := false; len(pending) > 0; sent = true {
		chunk := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if ctx.Err() != nil {
			ended := newEmailError("network_error", "The call's context ended, so the rest of the batch wasn't sent.", ctx.Err())
			run.skip(append(pending, chunk), ended)
			return s.batchFailed(run, ended)
		}
		if sent && s.isClosed() {
			closed := newEmailError("client_closed", "The PostHog client is shut down, so the rest of the batch wasn't sent.", ErrClosed)
			run.skip(append(pending, chunk), closed)
			return s.batchFailed(run, closed)
		}
		body := run.body(chunk)
		attempts, uncertain, lostBefore := 0, false, false
		// answered is PostHog's latest answer to an attempt after a lost one.
		var answered *EmailError
		err := s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
			if uncertain {
				run.lost(chunk)
				lostBefore = true
			}
			uncertain = false
			if attempts++; attempts > 1 && s.isClosed() {
				return newEmailError("client_closed", "The PostHog client is shut down, so it didn't retry this request of the batch.", ErrClosed)
			}
			reply, err := s.doFor(ctx, s.batchAttemptTimeout(), http.MethodPost, s.appHost+emailBatchPath, header, body)
			if err == nil {
				err = run.answer(chunk, reply)
			}
			uncertain = err != nil && mayHaveQueued(err)
			if lostBefore && err != nil && err.StatusCode != 0 {
				answered = err
			}
			return err
		})
		if err == nil {
			continue
		}
		if uncertain {
			run.lost(chunk)
			if answered != nil {
				for _, i := range chunk {
					run.results[i].LaterError = answered
				}
			}
		}
		if err.StatusCode == http.StatusRequestEntityTooLarge && err.Code == "content_too_large" && !uncertain && !slices.ContainsFunc(chunk, run.unconfirmed) {
			// The pipeline refused the data it copies to every recipient, so
			// smaller requests can still go out. A 400 content_too_large is the
			// content they all share, and a bare 413 is a proxy's limit, which no
			// split is sure to meet.
			if half := len(chunk) / 2; half > 0 {
				pending = append(pending, chunk[half:], chunk[:half:half])
				continue
			}
			alone := *err
			alone.Fix = "Make this recipient's email smaller."
			run.set(chunk[0], "failed", BatchEmailResult{Error: &alone})
			continue
		}
		run.stop(chunk, pending, err, uncertain)
		return s.batchFailed(run, err)
	}
	s.logWarnings(run.warnings)
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

func (s *emailSender) batchFailed(run *batchRun, err *EmailError) (*SendBatchResult, error) {
	s.logWarnings(run.warnings)
	return run.result(), err
}

func (s *emailSender) logWarnings(warnings []EmailWarning) {
	for _, warning := range warnings {
		s.logger.Warnf("PostHog email warning %s: %s", warning.Code, warning.Message)
	}
}

// batchReplyError is a batch request that failed as a whole. A refused
// recipient field names the caller's position in Recipients.
func batchReplyError(reply emailReply, indices []int) *EmailError {
	err := reply.error()
	err.Field = callerRecipientsField(err.Field, indices)
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

// callerRecipientsField turns "recipients__37__to" of a request into the
// caller's position, such as "recipients__137__to". A position the request
// doesn't have reads as "recipients".
func callerRecipientsField(field string, indices []int) string {
	rest, ok := strings.CutPrefix(field, "recipients__")
	if !ok {
		return field
	}
	position, tail, _ := strings.Cut(rest, "__")
	n, err := strconv.Atoi(position)
	if err != nil || n < 0 || n >= len(indices) || strconv.Itoa(n) != position {
		return "recipients"
	}
	if tail != "" {
		tail = "__" + tail
	}
	return "recipients__" + strconv.Itoa(indices[n]) + tail
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

const resendWithTheSameKey = "Send it again with the same IdempotencyKey within 24 hours, so it goes out once."

func unconfirmedEmail(messageID string) *EmailError {
	return &EmailError{Name: EmailErrorServiceUnavailable, Code: "delivery_unavailable", Message: "PostHog didn't confirm this email, so it may still go out.", Fix: resendWithTheSameKey, MessageID: messageID}
}

func recipientTooLarge() *EmailError {
	return &EmailError{Name: EmailErrorValidation, Code: "content_too_large", Message: "This recipient's email is larger than one batch request takes, so it wasn't sent.", Fix: "Send this recipient's email with Emails.Send.", Field: "Recipients"}
}
