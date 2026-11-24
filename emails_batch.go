package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	emailBatchPath       = "/api/messaging/send/email_batch/"
	emailStatusByKeyPath = "/api/messaging/emails/status/"
	maxBatchRecipients   = 100
	maxBatchBodyBytes    = 4 << 20
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
	// Never reuse a key for another email: after a lost answer, the client
	// reads the email by its key, and an earlier email with that key reads as
	// this one, "queued".
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
// Attachments or ScheduledAt: send those emails with Send.
type CodeEmailBatch struct {
	TemplateKey       string
	Subject           string
	HTML              string
	Text              string
	From              string
	ReplyTo           []string
	Sensitive         *bool
	Tracking          *EmailTracking
	CaptureEngagement *bool
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
	TemplateKey       string            `json:"template_key"`
	Subject           string            `json:"subject"`
	HTML              string            `json:"html,omitempty"`
	Text              string            `json:"text,omitempty"`
	From              string            `json:"from,omitempty"`
	ReplyTo           []string          `json:"reply_to,omitempty"`
	Sensitive         *bool             `json:"sensitive,omitempty"`
	Tracking          *EmailTracking    `json:"tracking,omitempty"`
	CaptureEngagement *bool             `json:"capture_engagement,omitempty"`
	Recipients        []json.RawMessage `json:"recipients"`
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
	if err := checkCodeContent("CodeEmailBatch", b.TemplateKey, b.Subject, b.HTML, b.Text, b.ReplyTo, nil, nil); err != nil {
		return nil, err
	}
	empty, err := encodeEmailBody(codeBatchBody{
		TemplateKey:       b.TemplateKey,
		Subject:           b.Subject,
		HTML:              b.HTML,
		Text:              b.Text,
		From:              b.From,
		ReplyTo:           b.ReplyTo,
		Sensitive:         b.Sensitive,
		Tracking:          b.Tracking,
		CaptureEngagement: b.CaptureEngagement,
		Recipients:        []json.RawMessage{},
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

type batchChunk struct {
	body    []byte
	indices []int
}

// batchRun is one Batch call: its recipients split into requests, and what
// PostHog answered for each recipient so far.
type batchRun struct {
	members    []batchMember
	chunks     []batchChunk
	maxRetries *int
	results    []BatchEmailResult
	warnings   []EmailWarning
	// maybeQueued are recipients an attempt may have queued without an
	// answer that says so. They stay "unconfirmed" until an answer queues them.
	maybeQueued map[int]bool
}

func newBatchRun(empty []byte, recipients []BatchRecipient, isTemplate bool, maxRetries *int) (*batchRun, *EmailError) {
	if len(recipients) == 0 {
		return nil, invalidEmail("invalid_recipients", "Recipients", "Recipients must hold at least one recipient.")
	}
	if maxRetries != nil && (*maxRetries < 0 || *maxRetries > maxEmailRetries) {
		return nil, invalidEmail("invalid_max_retries", "MaxRetries", "MaxRetries must be 0 to 9.")
	}
	run := &batchRun{maxRetries: maxRetries, results: make([]BatchEmailResult, len(recipients)), warnings: []EmailWarning{}, maybeQueued: map[int]bool{}}
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
	run.split(empty)
	return run, nil
}

func newBatchMember(i int, recipient BatchRecipient, isTemplate bool) (batchMember, *EmailError) {
	where := "Recipients[" + strconv.Itoa(i) + "]"
	refuse := func(code, field, message string) (batchMember, *EmailError) {
		return batchMember{}, invalidEmail(code, where+"."+field, message)
	}
	if strings.TrimSpace(recipient.To) == "" {
		return refuse("invalid_recipient", "To", where+": To must be one email address.")
	}
	key := recipient.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	} else if !idempotencyKeyPattern.MatchString(key) {
		return refuse("invalid_idempotency_key", "IdempotencyKey", where+": IdempotencyKey must be 1 to 255 visible ASCII characters without spaces.")
	}
	if recipient.TemplateData != nil && !isTemplate {
		return refuse("invalid_form", "TemplateData", where+" has TemplateData, but a CodeEmailBatch sends the same content to every recipient. Send personalized content with a TemplateEmailBatch or with Send.")
	}
	wire, err := encodeEmailBody(batchRecipientBody{To: recipient.To, DistinctID: recipient.DistinctID, IdempotencyKey: key, MessageData: recipient.TemplateData})
	if err != nil {
		return refuse("invalid_template_data", "TemplateData", where+": TemplateData must encode as JSON.")
	}
	return batchMember{to: recipient.To, key: key, wire: wire}, nil
}

// split packs the recipients, in order, into requests of at most 100
// recipients and maxBatchBodyBytes. A recipient too large for any request
// fails alone.
func (r *batchRun) split(empty []byte) {
	var current []int
	size := len(empty)
	flush := func() {
		if len(current) > 0 {
			r.chunks = append(r.chunks, r.chunk(empty, current))
		}
		current, size = nil, len(empty)
	}
	for i, member := range r.members {
		if len(empty)+len(member.wire) > maxBatchBodyBytes {
			r.set(i, "failed", BatchEmailResult{Error: recipientTooLarge()})
			continue
		}
		added := len(member.wire)
		if len(current) > 0 {
			added++
		}
		if len(current) > 0 && (len(current) == maxBatchRecipients || size+added > maxBatchBodyBytes) {
			flush()
			added = len(member.wire)
		}
		current = append(current, i)
		size += added
	}
	flush()
}

func (r *batchRun) chunk(empty []byte, indices []int) batchChunk {
	wires := make([][]byte, len(indices))
	for n, i := range indices {
		wires[n] = r.members[i].wire
	}
	body := append(append(append([]byte{}, empty[:len(empty)-len("]}")]...), bytes.Join(wires, []byte(","))...), "]}"...)
	return batchChunk{body: body, indices: indices}
}

func (r *batchRun) set(i int, status string, result BatchEmailResult) {
	result.Index, result.To, result.IdempotencyKey, result.Status = i, r.members[i].to, r.members[i].key, status
	r.results[i] = result
}

// lost marks a chunk whose attempt may have queued emails without an answer
// that says so.
func (r *batchRun) lost(chunk batchChunk) {
	for _, i := range chunk.indices {
		r.maybeQueued[i] = true
		messageID := r.results[i].MessageID
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: unconfirmedEmail(messageID)})
	}
}

// answer settles each recipient of the chunk from PostHog's answer, or
// returns why the request failed as a whole.
func (r *batchRun) answer(chunk batchChunk, reply emailReply) *EmailError {
	if !reply.accepted() {
		if reply.status >= http.StatusInternalServerError {
			r.lost(chunk)
		}
		return batchReplyError(reply, chunk.indices)
	}
	slots, _ := reply.fields["results"].([]interface{})
	if len(slots) != len(chunk.indices) {
		r.lost(chunk)
		return reply.apiError("PostHog answered the batch without a result for each recipient.")
	}
	for n, i := range chunk.indices {
		fields, _ := slots[n].(map[string]interface{})
		r.settle(i, emailReply{fields: fields})
	}
	for _, warning := range reply.warnings() {
		if !containsWarning(r.warnings, warning) {
			r.warnings = append(r.warnings, warning)
		}
	}
	return nil
}

func (r *batchRun) settle(i int, slot emailReply) {
	messageID := slot.text("message_id")
	_, hasError := slot.fields["error"]
	if !hasError && slot.text("status") == "queued" && messageID != "" {
		delete(r.maybeQueued, i)
		replayed, _ := slot.fields["replayed"].(bool)
		r.set(i, "queued", BatchEmailResult{MessageID: messageID, Replayed: replayed})
		return
	}
	answer := slotError(slot.fields["error"], messageID)
	switch {
	case r.maybeQueued[i]:
		earlier := r.results[i]
		if messageID == "" {
			messageID = earlier.MessageID
		}
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: earlier.Error, LaterError: answer})
	case answer.Code == "delivery_unavailable" || answer.Code == "idempotency_key_in_flight" || answer.Code == "api_error":
		r.maybeQueued[i] = true
		r.set(i, "unconfirmed", BatchEmailResult{MessageID: messageID, Error: answer})
	default:
		r.set(i, "failed", BatchEmailResult{MessageID: messageID, Error: answer})
	}
}

// stop records that the request of chunk position failed as a whole with
// err, so the batch stops there.
func (r *batchRun) stop(position int, err *EmailError) {
	for _, i := range r.chunks[position].indices {
		switch {
		case !r.maybeQueued[i]:
			r.set(i, "failed", BatchEmailResult{Error: err})
		case err.StatusCode != 0 && err.StatusCode < http.StatusInternalServerError:
			earlier := r.results[i]
			r.set(i, "unconfirmed", BatchEmailResult{MessageID: earlier.MessageID, Error: earlier.Error, LaterError: err})
		}
	}
	r.skipFrom(position+1, &EmailError{Name: EmailErrorApplication, Code: "not_attempted", Message: "An earlier request of the batch failed, so the client sent nothing for this recipient."})
}

func (r *batchRun) skipFrom(position int, err *EmailError) {
	for _, chunk := range r.chunks[position:] {
		for _, i := range chunk.indices {
			r.set(i, "not_attempted", BatchEmailResult{Error: err})
		}
	}
}

func (r *batchRun) result() *SendBatchResult {
	return &SendBatchResult{Results: r.results, Warnings: r.warnings}
}

func containsWarning(warnings []EmailWarning, warning EmailWarning) bool {
	for _, w := range warnings {
		if w == warning {
			return true
		}
	}
	return false
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
	for position, chunk := range run.chunks {
		if position > 0 && s.isClosed() {
			closed := newEmailError("client_closed", "The PostHog client is shut down, so the rest of the batch wasn't sent.", ErrClosed)
			run.skipFrom(position, closed)
			return s.batchFailed(run, closed)
		}
		attempts := 0
		err := s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
			if attempts++; attempts > 1 && s.isClosed() {
				return newEmailError("client_closed", "The PostHog client is shut down, so it didn't retry this request of the batch.", ErrClosed)
			}
			reply, err := s.doFor(ctx, s.batchAttemptTimeout(), http.MethodPost, s.appHost+emailBatchPath, header, chunk.body)
			if err != nil {
				run.lost(chunk)
				return err
			}
			return run.answer(chunk, reply)
		})
		if err != nil {
			run.stop(position, err)
			s.readLost(ctx, run)
			return s.batchFailed(run, err)
		}
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

// readLost reads each email a lost answer left without a message ID by its
// key, once and without retries. A 404 leaves it "unconfirmed"; any other
// failure stops the reads.
func (s *emailSender) readLost(ctx context.Context, run *batchRun) {
	for i := range run.members {
		if !run.maybeQueued[i] || run.results[i].MessageID != "" {
			continue
		}
		if s.isClosed() {
			return
		}
		url := s.appHost + emailStatusByKeyPath + "?" + url.Values{"idempotency_key": {run.members[i].key}}.Encode()
		reply, err := s.request(ctx, 0, http.MethodGet, url, s.header(), nil)
		if err != nil && err.StatusCode == http.StatusNotFound {
			continue
		}
		if err != nil {
			return
		}
		status, err := reply.emailStatus()
		if err != nil {
			return
		}
		delete(run.maybeQueued, i)
		run.set(i, "queued", BatchEmailResult{MessageID: status.ID})
	}
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

func unconfirmedEmail(messageID string) *EmailError {
	return &EmailError{Name: EmailErrorServiceUnavailable, Code: "delivery_unavailable", Message: "PostHog didn't confirm this email, so it may still go out. Send it again with the same IdempotencyKey within 24 hours, so it goes out once.", MessageID: messageID}
}

func recipientTooLarge() *EmailError {
	return &EmailError{Name: EmailErrorValidation, Code: "content_too_large", Message: "This recipient's email is larger than one batch request takes, so it wasn't sent.", Fix: "Send this recipient's email with Emails.Send.", Field: "Recipients"}
}
