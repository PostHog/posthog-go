package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	emailSendPath      = "/api/messaging/send/email/"
	emailStatusPath    = "/api/messaging/emails/"
	maxEmailRetryDelay = 30 * time.Second
	maxEmailRetries    = 9
	maxEmailReplyBytes = 1 << 20
	emailNotSent       = "no email was sent"
	emailStatusNotRead = "no email status was read"
	scheduledAtLayout  = "2006-01-02T15:04:05.000Z07:00"
	// maxEmailClockSkew is how far the client's clock may run behind
	// PostHog's before a send PostHog calls past looks early to the client.
	maxEmailClockSkew = time.Minute
)

var (
	idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)
	templateKeyPattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,400}$`)
	cloudAppHosts         = map[string]string{
		"https://us.i.posthog.com": "https://us.posthog.com",
		"https://us.posthog.com":   "https://us.posthog.com",
		"https://app.posthog.com":  "https://us.posthog.com",
		"https://eu.i.posthog.com": "https://eu.posthog.com",
		"https://eu.posthog.com":   "https://eu.posthog.com",
	}
)

// Emails sends transactional email, such as a password reset or a receipt, to
// one recipient with Send or to many with Batch, and reads the status of an
// email it sent.
//
// It needs Config.SecretKey, a project secret API key (phs_...) with the
// transactional_email:write scope, the "Send transactional email" preset in
// your project settings. A key with only transactional_email:read can read
// statuses but not send. Keys made with hog_flow:write still work. Requests go
// to Config.AppHost, or to the PostHog Cloud app host of Config.Endpoint.
//
// Every error is an *EmailError. Network errors, rate limits, an idempotency
// key in flight and an unavailable service are retried up to
// Config.EmailRequestMaxRetries times, with the same idempotency key.
//
// A send with ScheduledAt waits until that time, at most 7 days ahead. Only
// PostHog Cloud schedules emails: on any other app host, such as a
// self-hosted PostHog, a reverse proxy or a custom domain, Send returns an
// error with code "scheduling_requires_cloud" before any request. Get reads
// "scheduled" until the email goes out. Send can't cancel or move a
// scheduled email. To stop one before it goes out, disable the workflow
// PostHog accepted it through until the email reaches a final status. A
// TemplateEmail, and a CodeEmail whose TemplateKey named a saved template
// then, use that template's workflow. Any other CodeEmail goes through a
// workflow PostHog keeps out of the workflow list, which the workflows API
// can still disable by its ID.
type Emails interface {
	// Send queues one email, from a TemplateEmail made in PostHog or from a
	// CodeEmail your code rendered, or schedules it when it has a
	// ScheduledAt. A result means PostHog queued or scheduled the email, not
	// that it was delivered; its Status says which.
	Send(ctx context.Context, email Email) (*SendEmailResult, error)

	// Get reads the status of an email that Send queued, for 30 days after the
	// send. The error code is "not_found" when no email of the project has the
	// ID, when the email is older than 30 days, or for a few seconds after the
	// send.
	Get(ctx context.Context, messageID string) (*EmailStatus, error)

	// Batch sends the same email to up to 100 recipients, one email each, in
	// one request, from a TemplateEmailBatch, with TemplateData per
	// recipient, or a CodeEmailBatch. It refuses more than 100 recipients
	// before sending anything. It waits at least 45 seconds for the answer.
	// Every retry carries each recipient's IdempotencyKey, so a retry never
	// queues an email twice.
	//
	// The result holds one BatchEmailResult per recipient, in order: check
	// each Status. When PostHog refuses the request as a whole, Batch returns
	// that error with a result that holds every recipient. A 413
	// "content_too_large" means the batch is too large: its Fix says to split
	// the recipients into smaller batches, or, for one recipient, to make
	// that email smaller. A 400 "content_too_large" means the shared content
	// is too large, which splitting doesn't help. The client checks only the
	// recipient count, not the size. After a lost answer, recipients stay
	// "unconfirmed" even when PostHog refuses the retry, since the lost
	// attempt may have queued them. An "unconfirmed" email's Error says to
	// send it again with the same IdempotencyKey. The error code
	// "batch_unavailable" (409) means the project can't send batches yet,
	// and "not_found" that the template doesn't exist or batch sends are off
	// for the project: send each email the batch didn't queue with Send and
	// its recipient's IdempotencyKey, within 24 hours of the first request
	// that used the key, so it goes out once. A result is nil
	// only when the client sent nothing, such as for an argument it refuses
	// before any request.
	Batch(ctx context.Context, batch EmailBatch) (*SendBatchResult, error)
}

// Email is the content of one send: a TemplateEmail or a CodeEmail.
type Email interface {
	prepare() (emailSend, *EmailError)
}

// TemplateEmail sends an email template made transactional in PostHog, by its
// ID. The template sets its own sender, subject and content, and follows the
// project's engagement setting.
type TemplateEmail struct {
	// To is one recipient email address.
	To string
	// TemplateID is the UUID of the template.
	TemplateID string
	// TemplateData fills the template's {{ variables.* }} placeholders.
	TemplateData map[string]interface{}
	// DistinctID is the recipient's distinct ID in your product. The email's
	// events go to this person. Without it, they go to a person whose distinct
	// ID is the email address.
	DistinctID string
	// IdempotencyKey is 1 to 255 visible ASCII characters. A send with a key
	// that PostHog already accepted queues nothing and returns the first send's
	// result. Without it, the client makes a UUID that covers its own retries.
	// Pass a key derived from what the email is for, such as
	// "welcome:user_123", to cover retries outside this call too.
	IdempotencyKey string
	// MaxRetries overrides Config.EmailRequestMaxRetries for this send.
	// Ptr(0) turns retries off.
	MaxRetries *int
	// ScheduledAt sends the email at this time instead of now, at most 7 days
	// ahead, on PostHog Cloud only. PostHog keeps the time to the
	// millisecond, so the client rounds a finer time up to the next
	// millisecond. A time that has passed sends the email now. The zero time
	// sends now. When PostHog sends it now for a time more than a minute
	// ahead of this client's clock, also on a replay, Send returns
	// "schedule_not_confirmed". On the client's own retries, that minute
	// counts from the first attempt that may have sent the email, such as a
	// lost answer or a 5xx. Work out a relative time, such as an hour from now,
	// once, and send the same time when you send again with the
	// same IdempotencyKey: another time with the same key is refused with
	// "idempotency_key_reused".
	ScheduledAt time.Time
}

// CodeEmail sends content your code rendered, named by a key. PostHog sends
// the subject and bodies as is and never stores them; it keeps attachments,
// as Attachments says. To send a React email, render it to HTML in your app
// and pass the HTML.
type CodeEmail struct {
	// To is one recipient email address.
	To string
	// TemplateKey names the email, such as "password-reset", so its metrics,
	// logs and status group under it in PostHog. Letters, digits, hyphens and
	// underscores, at most 400.
	TemplateKey string
	Subject     string
	// HTML is the HTML body. Set HTML, Text or both.
	HTML string
	// Text is the plain-text body. Without it, PostHog derives it from HTML.
	Text string
	// From is a verified sender, such as "Acme <security@example.com>".
	// Without it, PostHog sends from the project's default sender.
	From    string
	ReplyTo []string
	// Cc are bare addresses, such as "finance@example.com", that get a copy
	// the other recipients see. At most 10.
	Cc []string
	// Bcc are bare addresses that get a copy the other recipients never see.
	// At most 10.
	Bcc []string
	// Attachments are files sent with the email: at most 10, and at most 40 MB
	// for the whole message after Base64 encoding, which leaves about 29 MB
	// for the files. Send checks both before any request. It then hashes each
	// file, uploads the files PostHog doesn't have yet straight to PostHog's
	// file storage, and sends the email naming them, so the network must
	// allow uploads to the storage host the upload URL names. A file PostHog
	// already has isn't uploaded again. PostHog refuses file types that
	// mailbox providers block, such as .exe. PostHog can't fetch a file from a
	// path or URL: read the file and pass its bytes. PostHog keeps each
	// uploaded file in its file storage for its retention period, and a later
	// send of the same bytes, from any of the project's code, reuses it.
	Attachments []EmailAttachment
	// Sensitive marks an email that carries a login link, a reset token or a
	// code. PostHog keeps a sensitive email's links unchanged, leaves its
	// subject off engagement events and keeps its content out of logs. Nil
	// takes the setting of the saved template whose key is TemplateKey, or
	// true when no template has that key.
	Sensitive *bool
	// Tracking says what PostHog tracks for this email. Nil keeps the
	// defaults: PostHog counts opens and captures engagement events, and
	// rewrites links to count clicks only when the email isn't Sensitive. Use
	// EmailTrackingOn, EmailTrackingOff or EmailTrackingWith.
	Tracking *EmailTracking
	// CaptureEngagement says whether PostHog captures the email's engagement
	// events as events of the recipient. Nil means true. A project whose
	// admin turned engagement capture off captures nothing, whatever the send
	// asks. PostHog refuses a send that sets both CaptureEngagement and
	// Tracking.
	//
	// Deprecated: Use Tracking with EmailTrackingOptions.Engagement.
	CaptureEngagement *bool
	// DistinctID is the recipient's distinct ID in your product. The email's
	// events go to this person. Without it, they go to a person whose distinct
	// ID is the email address.
	DistinctID string
	// IdempotencyKey is 1 to 255 visible ASCII characters. See
	// TemplateEmail.IdempotencyKey.
	IdempotencyKey string
	// MaxRetries overrides Config.EmailRequestMaxRetries for this send.
	// Ptr(0) turns retries off.
	MaxRetries *int
	// ScheduledAt sends the email at this time instead of now. See
	// TemplateEmail.ScheduledAt.
	ScheduledAt time.Time
}

// EmailAttachment is a file sent with a CodeEmail.
type EmailAttachment struct {
	// Filename is the name the recipient sees, such as "invoice.pdf", with no
	// path.
	Filename string
	// Content is the file's bytes.
	Content []byte
	// ContentType is a MIME type, such as "application/pdf". Without it, the
	// type follows from Filename.
	ContentType string
	// ContentID shows the file inline: the HTML references it as
	// <img src="cid:logo"> when ContentID is "logo".
	ContentID string
}

// EmailTracking says what PostHog tracks for a CodeEmail. Make one with
// EmailTrackingOn, EmailTrackingOff or EmailTrackingWith.
type EmailTracking struct {
	all     *bool
	options EmailTrackingOptions
}

// EmailTrackingOptions turns each kind of tracking on or off. A nil field
// keeps its default.
type EmailTrackingOptions struct {
	// Opens says whether PostHog adds its open pixel to count opens. Nil
	// means true. With Opens false, PostHog counts no clicks, and refuses
	// Clicks true.
	Opens *bool `json:"opens,omitempty"`
	// Clicks says whether PostHog rewrites the email's links to count clicks.
	// Nil means true when the email isn't Sensitive and false when it is.
	// Turn it on only when the links are safe to send through a redirect, so
	// not for a login or password reset link. A link with a data-ph-no-track
	// attribute is never rewritten.
	Clicks *bool `json:"clicks,omitempty"`
	// Engagement says whether PostHog captures the email's engagement events,
	// such as opens and clicks, as events of the recipient. Nil means true.
	Engagement *bool `json:"engagement,omitempty"`
}

// EmailTrackingOn tracks opens and clicks and captures engagement events.
// Use it only when the email's links are safe to send through a redirect.
func EmailTrackingOn() *EmailTracking {
	return &EmailTracking{all: Ptr(true)}
}

// EmailTrackingOff turns off every kind of tracking, including kinds PostHog
// adds later. PostHog then can't count the email's opens and clicks, and
// captures no engagement events.
func EmailTrackingOff() *EmailTracking {
	return &EmailTracking{all: Ptr(false)}
}

// EmailTrackingWith sets each kind of tracking on its own.
func EmailTrackingWith(options EmailTrackingOptions) *EmailTracking {
	return &EmailTracking{options: options}
}

// MarshalJSON encodes the tracking as true, false or an object of the kinds
// that are set.
func (t EmailTracking) MarshalJSON() ([]byte, error) {
	if t.all != nil {
		return json.Marshal(*t.all)
	}
	return json.Marshal(t.options)
}

// SendEmailResult is an email that PostHog queued or scheduled.
type SendEmailResult struct {
	// ID is the email's message ID.
	ID string
	// Replayed is true when PostHog had already accepted this idempotency key
	// and returned the first send's result.
	Replayed bool
	// Warnings are logged through Config.Logger as well.
	Warnings []EmailWarning
	// Status is "scheduled" when the email waits for ScheduledAt, and
	// "queued" when it goes out now, also when the ScheduledAt it was sent
	// with had already passed.
	Status string
	// ScheduledAt is when a scheduled email goes out, in UTC, and the zero
	// time when the email goes out now.
	ScheduledAt time.Time
}

// EmailWarning is a warning PostHog returned with a queued email.
type EmailWarning struct {
	Code    string
	Message string
	Fix     string
}

// EmailStatus is what happened to an email that Emails.Send queued.
type EmailStatus struct {
	// ID is the message ID that Send returned.
	ID string
	// Status is "scheduled", "queued", "sent", "failed", "delivered",
	// "bounced" or "complained" today. A scheduled email reads "scheduled"
	// until it goes out. Later SDK versions can see values this list does not
	// have yet.
	Status string
	// CreatedAt is when PostHog queued the email.
	CreatedAt time.Time
	// StatusAt is when the email reached its current status.
	StatusAt time.Time
	// Reason says why a failed email was not sent, such as "provider_error" or
	// "recipient_suppressed", or gives the bounce type of a bounced email:
	// "hard", "soft" or "unknown". It is empty otherwise.
	Reason string
}

type emailSend struct {
	body           []byte
	attachments    []attachment
	idempotencyKey string
	maxRetries     *int
	scheduledAt    time.Time
}

type templateEmailBody struct {
	TemplateID  string                 `json:"template_id"`
	To          string                 `json:"to"`
	MessageData map[string]interface{} `json:"message_data"`
	DistinctID  string                 `json:"distinct_id,omitempty"`
	ScheduledAt string                 `json:"scheduled_at,omitempty"`
}

type codeEmailBody struct {
	To                string         `json:"to"`
	TemplateKey       string         `json:"template_key"`
	Subject           string         `json:"subject"`
	HTML              string         `json:"html,omitempty"`
	Text              string         `json:"text,omitempty"`
	From              string         `json:"from,omitempty"`
	ReplyTo           []string       `json:"reply_to,omitempty"`
	Cc                []string       `json:"cc,omitempty"`
	Bcc               []string       `json:"bcc,omitempty"`
	Attachments       []attachment   `json:"attachments,omitempty"`
	Sensitive         *bool          `json:"sensitive,omitempty"`
	Tracking          *EmailTracking `json:"tracking,omitempty"`
	CaptureEngagement *bool          `json:"capture_engagement,omitempty"`
	DistinctID        string         `json:"distinct_id,omitempty"`
	ScheduledAt       string         `json:"scheduled_at,omitempty"`
}

func (e TemplateEmail) prepare() (emailSend, *EmailError) {
	if err := checkRecipient(e.To); err != nil {
		return emailSend{}, err
	}
	if !isHyphenatedUUID(e.TemplateID) {
		return emailSend{}, invalidEmail("invalid_template_id", "TemplateID", "TemplateID must be the template's UUID.")
	}
	messageData := e.TemplateData
	if messageData == nil {
		messageData = map[string]interface{}{}
	}
	scheduledAt := ceilToMillisecond(e.ScheduledAt)
	body, err := encodeEmailBody(templateEmailBody{
		TemplateID:  strings.ToLower(e.TemplateID),
		To:          e.To,
		MessageData: messageData,
		DistinctID:  e.DistinctID,
		ScheduledAt: formatScheduledAt(scheduledAt),
	})
	if err != nil {
		return emailSend{}, invalidEmail("invalid_template_data", "TemplateData", "TemplateData must encode as JSON.")
	}
	return newEmailSend(body, e.IdempotencyKey, e.MaxRetries, scheduledAt)
}

func (e CodeEmail) prepare() (emailSend, *EmailError) {
	if err := checkRecipient(e.To); err != nil {
		return emailSend{}, err
	}
	if err := checkCodeContent("CodeEmail", e.TemplateKey, e.Subject, e.HTML, e.Text, e.ReplyTo); err != nil {
		return emailSend{}, err
	}
	if err := checkAddresses("Cc", "invalid_cc", e.Cc); err != nil {
		return emailSend{}, err
	}
	if err := checkAddresses("Bcc", "invalid_bcc", e.Bcc); err != nil {
		return emailSend{}, err
	}
	attachments, attachmentsErr := checkAttachments(e.Attachments, e.Subject, e.HTML, e.Text)
	if attachmentsErr != nil {
		return emailSend{}, attachmentsErr
	}
	scheduledAt := ceilToMillisecond(e.ScheduledAt)
	body, err := encodeEmailBody(codeEmailBody{
		To:                e.To,
		TemplateKey:       e.TemplateKey,
		Subject:           e.Subject,
		HTML:              e.HTML,
		Text:              e.Text,
		From:              e.From,
		ReplyTo:           e.ReplyTo,
		Cc:                e.Cc,
		Bcc:               e.Bcc,
		Attachments:       attachments,
		Sensitive:         e.Sensitive,
		Tracking:          e.Tracking,
		CaptureEngagement: e.CaptureEngagement,
		DistinctID:        e.DistinctID,
		ScheduledAt:       formatScheduledAt(scheduledAt),
	})
	if err != nil {
		return emailSend{}, newEmailError("unexpected_error", "The client could not encode the email.", err)
	}
	send, sendErr := newEmailSend(body, e.IdempotencyKey, e.MaxRetries, scheduledAt)
	send.attachments = attachments
	return send, sendErr
}

// checkCodeContent checks what a CodeEmail or a CodeEmailBatch, named by
// form, sends to every recipient.
func checkCodeContent(form, templateKey, subject, html, text string, replyTo []string) *EmailError {
	if !templateKeyPattern.MatchString(templateKey) {
		return invalidEmail("invalid_template_key", "TemplateKey", "TemplateKey can only have letters, digits, hyphens (-) and underscores (_), at most 400.")
	}
	if subject == "" {
		return invalidEmail("invalid_form", "Subject", "A "+form+" needs a Subject.")
	}
	if html == "" && text == "" {
		return invalidEmail("invalid_form", "HTML", "A "+form+" needs HTML, Text or both.")
	}
	return checkAddresses("ReplyTo", "invalid_reply_to", replyTo)
}

func checkAddresses(field, code string, addresses []string) *EmailError {
	for _, address := range addresses {
		if strings.TrimSpace(address) == "" {
			return invalidEmail(code, field, field+" must not hold a blank address.")
		}
	}
	return nil
}

func checkRecipient(to string) *EmailError {
	if strings.TrimSpace(to) == "" {
		return invalidEmail("invalid_recipient", "To", "To must be one email address.")
	}
	return nil
}

// ceilToMillisecond rounds a time up to PostHog's millisecond precision, in
// UTC, so a scheduled email never goes out before the time asked for.
func ceilToMillisecond(at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	truncated := at.Truncate(time.Millisecond)
	if !truncated.Equal(at) {
		truncated = truncated.Add(time.Millisecond)
	}
	if truncated.IsZero() {
		// The zero time means "send now", which would drop the time and skip
		// the Cloud check. Only a time in year 0 lands here, long past, so the
		// next millisecond sends the email now just the same.
		truncated = truncated.Add(time.Millisecond)
	}
	return truncated.UTC()
}

func formatScheduledAt(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(scheduledAtLayout)
}

func newEmailSend(body []byte, idempotencyKey string, maxRetries *int, scheduledAt time.Time) (emailSend, *EmailError) {
	idempotencyKey, err := idempotencyKeyOrNew(idempotencyKey)
	if err != nil {
		return emailSend{}, err
	}
	if err := checkMaxRetries(maxRetries); err != nil {
		return emailSend{}, err
	}
	return emailSend{body: body, idempotencyKey: idempotencyKey, maxRetries: maxRetries, scheduledAt: scheduledAt}, nil
}

func idempotencyKeyOrNew(key string) (string, *EmailError) {
	if key == "" {
		return uuid.NewString(), nil
	}
	if !idempotencyKeyPattern.MatchString(key) {
		return "", invalidEmail("invalid_idempotency_key", "IdempotencyKey", "IdempotencyKey must be 1 to 255 visible ASCII characters without spaces.")
	}
	return key, nil
}

func checkMaxRetries(maxRetries *int) *EmailError {
	if maxRetries != nil && (*maxRetries < 0 || *maxRetries > maxEmailRetries) {
		return invalidEmail("invalid_max_retries", "MaxRetries", "MaxRetries must be 0 to 9.")
	}
	return nil
}

// emailSender implements Emails. It owns the attempt loop, so every attempt of
// a send carries the same idempotency key, and it tracks requests in flight so
// that Close waits for them.
type emailSender struct {
	disabled     bool
	appHost      string
	secretKey    string
	timeout      time.Duration
	maxRetries   int
	migratedFrom string
	http         http.Client
	logger       Logger
	backoff      func(attempt int) time.Duration
	// now is the client's clock, which times how long an upload URL lives.
	now func() time.Time
	// allowInsecureUploads lets attachments upload to an http URL.
	allowInsecureUploads bool

	// ctx ends when Close runs out of time, which aborts requests and retry waits.
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	inFlight sync.WaitGroup
}

func newEmailSender(config Config, transport http.RoundTripper) *emailSender {
	ctx, cancel := context.WithCancel(context.Background())
	return &emailSender{
		appHost:      resolveEmailAppHost(config.Endpoint, config.AppHost),
		secretKey:    config.effectiveSecretKey(),
		timeout:      config.EmailRequestTimeout,
		maxRetries:   *config.EmailRequestMaxRetries,
		migratedFrom: config.MigratedFrom,
		http: http.Client{
			Transport:     transport,
			CheckRedirect: keepSecretKeyAndMethodOnRedirect,
		},
		logger:  config.Logger,
		backoff: NewBackoff(time.Second, 2, 0.2, maxEmailRetryDelay).Duration,
		now:     config.now,

		allowInsecureUploads: config.AllowInsecureAppHost,
		ctx:                  ctx,
		cancel:               cancel,
	}
}

// keepSecretKeyAndMethodOnRedirect answers a redirect with the redirect itself,
// since following it would move the secret key or turn a send into a GET.
func keepSecretKeyAndMethodOnRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func newDisabledEmailSender() *emailSender {
	return &emailSender{disabled: true}
}

// resolveEmailAppHost returns the app host for email requests, or "" when the
// client can't tell it. PostHog Cloud ingestion hosts don't serve the app API,
// so they map to their region's app host.
func resolveEmailAppHost(endpoint, appHost string) string {
	if appHost = strings.TrimRight(appHost, "/"); appHost != "" {
		if cloud, ok := cloudAppHosts[canonicalHTTPSOrigin(appHost)]; ok {
			return cloud
		}
		return appHost
	}
	return cloudAppHosts[canonicalHTTPSOrigin(endpoint)]
}

// canonicalHTTPSOrigin spells an https origin the way cloudAppHosts does:
// a lower-case host without the default port or a trailing slash.
func canonicalHTTPSOrigin(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || strings.Trim(parsed.Path, "/") != "" {
		return rawURL
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return rawURL
	}
	return "https://" + strings.ToLower(parsed.Hostname())
}

func isHyphenatedUUID(id string) bool {
	return len(id) == 36 && uuid.Validate(id) == nil
}

func (s *emailSender) Send(ctx context.Context, email Email) (*SendEmailResult, error) {
	if isNilEmail(email) {
		return nil, newEmailError("invalid_form", "Send needs a TemplateEmail or a CodeEmail.", nil)
	}
	send, err := email.prepare()
	if err != nil {
		return nil, err
	}
	finish, err := s.start(emailNotSent, "Sending email needs Config.SecretKey, a project secret API key with the transactional_email:write scope.")
	if err != nil {
		return nil, err
	}
	defer finish()
	if !send.scheduledAt.IsZero() && cloudAppHosts[s.appHost] != s.appHost {
		return nil, schedulingRequiresCloud(s.appHost)
	}

	maxRetries := s.maxRetries
	if send.maxRetries != nil {
		maxRetries = *send.maxRetries
	}
	if len(send.attachments) > 0 {
		if err := s.uploadAttachments(ctx, maxRetries, send.attachments); err != nil {
			return nil, err
		}
	}
	header := s.header()
	header.Set("Content-Type", "application/json")
	header.Set("Idempotency-Key", send.idempotencyKey)
	var reply emailReply
	// queuedBy is when the first attempt that may have queued the email
	// began, the earliest PostHog could have chosen to send it now. A later
	// answer can't move it, a first answer neither, since PostHog answers a
	// retry as a first send when its key lookup runs late.
	var queuedBy time.Time
	err = s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
		var err *EmailError
		began := s.now()
		reply, err = s.do(ctx, http.MethodPost, s.appHost+emailSendPath, header, send.body)
		if err == nil && !reply.accepted() {
			err = reply.error()
		}
		if queuedBy.IsZero() && (err == nil || mayHaveQueued(err)) {
			queuedBy = began
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	result, err := reply.sendResult()
	if err != nil {
		return nil, err
	}
	s.logWarnings(result.Warnings)
	if !send.scheduledAt.IsZero() {
		if err := reply.checkSchedule(result, send.scheduledAt, queuedBy); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *emailSender) Get(ctx context.Context, messageID string) (*EmailStatus, error) {
	if !isHyphenatedUUID(messageID) {
		return nil, newEmailError("invalid_message_id", "messageID must be the message ID that Send returned.", nil)
	}
	finish, err := s.start(emailStatusNotRead, "Reading an email status needs Config.SecretKey, a project secret API key with the transactional_email:read or transactional_email:write scope.")
	if err != nil {
		return nil, err
	}
	defer finish()

	url := s.appHost + emailStatusPath + strings.ToLower(messageID) + "/"
	reply, err := s.request(ctx, s.maxRetries, http.MethodGet, url, s.header(), nil)
	if err != nil {
		return nil, err
	}
	status, err := reply.emailStatus()
	if err != nil {
		return nil, err
	}
	return status, nil
}

// encodeEmailBody encodes a request body with the standard library, keeping
// HTML as written. goccy/go-json's MarshalNoEscape hides its argument from
// escape analysis, so a body built on the stack can move while it encodes;
// the Go 1.21 race tests caught it reading such a body as garbage.
func encodeEmailBody(body interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// isNilEmail reports a nil Email or EmailBatch, including a nil pointer to
// one of their forms.
func isNilEmail(email interface{}) bool {
	if email == nil {
		return true
	}
	value := reflect.ValueOf(email)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// start admits one request into the in-flight set, or says why it can't run.
// The caller calls finish when the request has settled.
func (s *emailSender) start(nothingDone, keyNeeded string) (finish func(), err *EmailError) {
	if s.disabled {
		return nil, newEmailError("client_disabled", "The PostHog client is disabled, so "+nothingDone+".", ErrSDKDisabled)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, newEmailError("client_closed", "The PostHog client is shut down, so "+nothingDone+".", ErrClosed)
	}
	if s.appHost == "" {
		return nil, newEmailError("missing_app_host", "The client's Endpoint is not PostHog Cloud, so the client can't tell where to send email. Set Config.AppHost to your PostHog app host.", nil)
	}
	if s.secretKey == "" {
		return nil, newEmailError("missing_secret_key", keyNeeded, nil)
	}
	s.inFlight.Add(1)
	return s.inFlight.Done, nil
}

func (s *emailSender) header() http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.secretKey)
	header.Set("Accept", "application/json")
	header.Set("User-Agent", SDKName+"/"+getVersion())
	if s.migratedFrom != "" {
		header.Set("X-PostHog-Migrated-From", s.migratedFrom)
	}
	return header
}

// request sends the request until PostHog accepts it, retrying what waiting
// can fix. Every attempt carries the same header, so a send's idempotency key
// holds across retries. A reply it returns is always a 2xx.
func (s *emailSender) request(ctx context.Context, maxRetries int, method, url string, header http.Header, body []byte) (emailReply, *EmailError) {
	var reply emailReply
	err := s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
		var err *EmailError
		reply, err = s.do(ctx, method, url, header, body)
		if err == nil && !reply.accepted() {
			err = reply.error()
		}
		return err
	})
	if err != nil {
		return emailReply{}, err
	}
	return reply, nil
}

// retry runs attempt until it succeeds, retrying what waiting can fix. Close
// running out of time ends the attempt and the wait before the next one.
func (s *emailSender) retry(ctx context.Context, maxRetries int, attempt func(context.Context) *EmailError) *EmailError {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()
	for n := 0; ; n++ {
		err := attempt(ctx)
		if err == nil {
			return nil
		}
		delay, retry := s.retryDelay(err, n, maxRetries)
		if !retry {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return err.interruptedBy(ctx.Err())
		}
	}
}

func (s *emailSender) retryDelay(err *EmailError, attempt, maxRetries int) (time.Duration, bool) {
	if attempt >= maxRetries || !err.isRetryable() || asksForALongerWaitThanWeRetry(err) {
		return 0, false
	}
	delay := s.backoff(attempt)
	if err.RetryAfter > delay {
		delay = err.RetryAfter
	}
	return delay, true
}

// keysInFlight is PostHog's 409 for keys an earlier request still holds.
func keysInFlight(err *EmailError) bool {
	return err.Code == "idempotency_key_in_flight" && err.StatusCode == http.StatusConflict
}

// mayHaveQueued is an attempt's error after which PostHog may still have
// queued the email: no answer, a 5xx, a 2xx the client couldn't read, or a
// key an earlier request still holds. A cut-off 4xx other than a 409 is
// still a refusal.
func mayHaveQueued(err *EmailError) bool {
	cutOff := err.transportFailed() && (err.StatusCode < http.StatusBadRequest || err.StatusCode == http.StatusConflict)
	return cutOff || keysInFlight(err) || err.StatusCode >= http.StatusInternalServerError || (err.StatusCode >= 200 && err.StatusCode < 300)
}

// asksForALongerWaitThanWeRetry is a Retry-After past the longest retry wait.
// Retrying sooner would break the server's rate limit, so the error goes back
// to the caller at once.
func asksForALongerWaitThanWeRetry(err *EmailError) bool {
	return err.RetryAfter > maxEmailRetryDelay
}

func (s *emailSender) do(ctx context.Context, method, url string, header http.Header, body []byte) (emailReply, *EmailError) {
	return s.doFor(ctx, s.timeout, method, url, header, body)
}

func (s *emailSender) doFor(ctx context.Context, timeout time.Duration, method, url string, header http.Header, body []byte) (emailReply, *EmailError) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return emailReply{}, newEmailError("unexpected_error", "The client could not build the request.", err)
	}
	req.Header = header
	res, err := s.http.Do(req)
	if err != nil {
		return emailReply{}, newEmailError("network_error", "The request to PostHog failed.", err)
	}
	defer res.Body.Close()

	reply := emailReply{status: res.StatusCode}
	reply.retryAfter, _ = parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxEmailReplyBytes))
	if err != nil {
		return emailReply{}, cutOffAnswer(err, reply.status, reply.retryAfter)
	}
	reply.fields = jsonObjectOrNothing(raw)
	return reply, nil
}

// cutOffAnswer is an answer that broke off mid-body. It can't say what PostHog
// decided: a send may already be queued, or a refusal may have been
// retryable. So it is a retryable network error, which the idempotency key
// keeps safe. Its status line and a Retry-After in its headers still hold.
func cutOffAnswer(cause error, status int, retryAfter time.Duration) *EmailError {
	err := newEmailError("network_error", "Reading PostHog's answer failed.", cause)
	err.StatusCode = status
	err.RetryAfter = retryAfter
	return err
}

// jsonObjectOrNothing decodes a whole JSON object. A body that isn't one as a
// whole says nothing, even when its start parsed.
func jsonObjectOrNothing(raw []byte) map[string]interface{} {
	var fields map[string]interface{}
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}

// close stops new requests. Requests already admitted keep running.
func (s *emailSender) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// drain waits for every request in flight, retries included, to settle. When
// ctx ends first, it aborts them and returns ctx's error once they settled.
func (s *emailSender) drain(ctx context.Context) error {
	settled := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(settled)
	}()
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		s.cancel()
		<-settled
		return ctx.Err()
	}
}

type emailReply struct {
	status     int
	fields     map[string]interface{}
	retryAfter time.Duration
}

func (r emailReply) accepted() bool { return r.status >= 200 && r.status < 300 }

func (r emailReply) text(field string) string {
	value, _ := r.fields[field].(string)
	return value
}

func (r emailReply) error() *EmailError {
	code := r.text("code")
	if code == "" {
		code = "api_error"
	}
	message := r.text("detail")
	if message == "" {
		message = "PostHog answered HTTP " + strconv.Itoa(r.status) + "."
	}
	return &EmailError{
		Name:       emailErrorName(r.status, code),
		Code:       code,
		Message:    message,
		StatusCode: r.status,
		Why:        r.text("why"),
		Fix:        r.text("fix"),
		DocsURL:    r.text("docs_url"),
		Field:      r.text("attr"),
		RetryAfter: r.retryAfter,
	}
}

func (r emailReply) apiError(message string) *EmailError {
	return &EmailError{Name: emailErrorName(r.status, "api_error"), Code: "api_error", Message: message, StatusCode: r.status, RetryAfter: r.retryAfter}
}

func (r emailReply) sendResult() (*SendEmailResult, *EmailError) {
	id := r.text("message_id")
	if id == "" {
		return nil, r.apiError("PostHog answered without a message ID.")
	}
	replayed, _ := r.fields["replayed"].(bool)
	return &SendEmailResult{ID: id, Replayed: replayed, Warnings: r.warnings(), Status: "queued"}, nil
}

func (r emailReply) warnings() []EmailWarning {
	result := []EmailWarning{}
	warnings, _ := r.fields["warnings"].([]interface{})
	for _, item := range warnings {
		fields, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		warning := emailReply{fields: fields}
		message := warning.text("detail")
		if message == "" {
			message = warning.text("message")
		}
		code := warning.text("code")
		if code == "" {
			code = "warning"
		}
		result = append(result, EmailWarning{Code: code, Message: message, Fix: warning.text("fix")})
	}
	return result
}

// checkSchedule accepts the answer to a scheduled send only when it confirms
// the requested time, or says the email goes out now for a time that has
// passed. A PostHog that dropped the time answers "queued" too, so a
// "queued" for a time more than maxEmailClockSkew ahead of queuedBy is
// unconfirmed, on a first send and a replay alike. Within that minute,
// "queued" with a null scheduled_at is accepted, because PostHog's clock may
// already have been past it.
func (r emailReply) checkSchedule(result *SendEmailResult, requested, queuedBy time.Time) *EmailError {
	answered, hasTime := r.fields["scheduled_at"]
	switch r.text("status") {
	case "scheduled":
		at, err := time.Parse(time.RFC3339Nano, r.text("scheduled_at"))
		if err == nil && at.Equal(requested) {
			result.Status = "scheduled"
			result.ScheduledAt = requested
			return nil
		}
	case "queued":
		if hasTime && answered == nil && !requested.After(queuedBy.Add(maxEmailClockSkew)) {
			return nil
		}
	}
	return scheduleNotConfirmed(result.ID, r.status)
}

func (r emailReply) emailStatus() (*EmailStatus, *EmailError) {
	createdAt, createdErr := time.Parse(time.RFC3339Nano, r.text("created_at"))
	statusAt, statusErr := time.Parse(time.RFC3339Nano, r.text("status_at"))
	status := &EmailStatus{ID: r.text("id"), Status: r.text("status"), CreatedAt: createdAt, StatusAt: statusAt, Reason: r.text("reason")}
	if status.ID == "" || status.Status == "" || createdErr != nil || statusErr != nil {
		return nil, r.apiError("PostHog answered without an email status.")
	}
	return status, nil
}
