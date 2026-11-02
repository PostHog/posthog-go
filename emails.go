package posthog

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
)

const (
	emailSendPath      = "/api/projects/@current/messaging/send/email/"
	emailStatusPath    = "/api/projects/@current/messaging/emails/"
	maxEmailRetryDelay = 30 * time.Second
	maxEmailReplyBytes = 1 << 20
	emailNotSent       = "no email was sent"
	emailStatusNotRead = "no email status was read"
)

var (
	emailUUIDPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
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
// one recipient per call, and reads the status of an email it sent.
//
// It needs Config.SecretKey, a project secret API key (phs_...) with the
// hog_flow:write scope. A key with only hog_flow:read can read statuses but
// not send. Requests go to Config.AppHost, or to the PostHog Cloud app host of
// Config.Endpoint.
//
// Every error is an *EmailError. Network errors, rate limits, an idempotency
// key in flight and an unavailable service are retried up to
// Config.EmailRequestMaxRetries times, with the same idempotency key.
type Emails interface {
	// Send queues one email, from a TemplateEmail made in PostHog or from a
	// CodeEmail your code rendered. A result means PostHog queued the email,
	// not that it was delivered.
	Send(ctx context.Context, email Email) (*SendEmailResult, error)

	// Get reads the status of an email that Send queued, for 30 days after the
	// send. The error code is "not_found" when no email of the project has the
	// ID, when the email is older than 30 days, or for a few seconds after the
	// send.
	Get(ctx context.Context, messageID string) (*EmailStatus, error)
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
}

// CodeEmail sends content your code rendered, named by a key. PostHog sends
// it as is and never stores it. To send a React email, render it to HTML in
// your app and pass the HTML.
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
	// CaptureEngagement says whether PostHog captures the email's engagement
	// events, such as opens and clicks, as events of the recipient. Nil means
	// true. A project whose admin turned engagement capture off captures
	// nothing, whatever the send asks.
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
}

// SendEmailResult is an email that PostHog queued.
type SendEmailResult struct {
	// ID is the queued email's message ID.
	ID string
	// Replayed is true when PostHog had already accepted this idempotency key
	// and returned the first send's result.
	Replayed bool
	// Warnings are logged through Config.Logger as well.
	Warnings []EmailWarning
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
	// Status is "queued", "sent", "failed", "delivered", "bounced" or
	// "complained" today. Later SDK versions can see values this list does not
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
	idempotencyKey string
	maxRetries     *int
}

type templateEmailBody struct {
	TemplateID  string                 `json:"template_id"`
	To          string                 `json:"to"`
	MessageData map[string]interface{} `json:"message_data"`
	DistinctID  string                 `json:"distinct_id,omitempty"`
}

type codeEmailBody struct {
	To                string   `json:"to"`
	TemplateKey       string   `json:"template_key"`
	Subject           string   `json:"subject"`
	HTML              string   `json:"html,omitempty"`
	Text              string   `json:"text,omitempty"`
	From              string   `json:"from,omitempty"`
	ReplyTo           []string `json:"reply_to,omitempty"`
	CaptureEngagement bool     `json:"capture_engagement"`
	DistinctID        string   `json:"distinct_id,omitempty"`
}

func (e TemplateEmail) prepare() (emailSend, *EmailError) {
	if err := checkRecipient(e.To); err != nil {
		return emailSend{}, err
	}
	if !emailUUIDPattern.MatchString(e.TemplateID) {
		return emailSend{}, invalidEmail("invalid_template_id", "TemplateID", "TemplateID must be the template's UUID.")
	}
	messageData := e.TemplateData
	if messageData == nil {
		messageData = map[string]interface{}{}
	}
	body, err := json.MarshalNoEscape(templateEmailBody{
		TemplateID:  strings.ToLower(e.TemplateID),
		To:          e.To,
		MessageData: messageData,
		DistinctID:  e.DistinctID,
	})
	if err != nil {
		return emailSend{}, invalidEmail("invalid_template_data", "TemplateData", "TemplateData must encode as JSON.")
	}
	return newEmailSend(body, e.IdempotencyKey, e.MaxRetries)
}

func (e CodeEmail) prepare() (emailSend, *EmailError) {
	if err := checkRecipient(e.To); err != nil {
		return emailSend{}, err
	}
	if !templateKeyPattern.MatchString(e.TemplateKey) {
		return emailSend{}, invalidEmail("invalid_template_key", "TemplateKey", "TemplateKey can only have letters, digits, hyphens (-) and underscores (_), at most 400.")
	}
	if e.Subject == "" {
		return emailSend{}, invalidEmail("invalid_form", "Subject", "A CodeEmail needs a Subject.")
	}
	if e.HTML == "" && e.Text == "" {
		return emailSend{}, invalidEmail("invalid_form", "HTML", "A CodeEmail needs HTML, Text or both.")
	}
	for _, address := range e.ReplyTo {
		if strings.TrimSpace(address) == "" {
			return emailSend{}, invalidEmail("invalid_reply_to", "ReplyTo", "ReplyTo must not hold a blank address.")
		}
	}
	body, err := json.MarshalNoEscape(codeEmailBody{
		To:                e.To,
		TemplateKey:       e.TemplateKey,
		Subject:           e.Subject,
		HTML:              e.HTML,
		Text:              e.Text,
		From:              e.From,
		ReplyTo:           e.ReplyTo,
		CaptureEngagement: e.CaptureEngagement == nil || *e.CaptureEngagement,
		DistinctID:        e.DistinctID,
	})
	if err != nil {
		return emailSend{}, newEmailError("unexpected_error", "The client could not encode the email.", err)
	}
	return newEmailSend(body, e.IdempotencyKey, e.MaxRetries)
}

func checkRecipient(to string) *EmailError {
	if strings.TrimSpace(to) == "" {
		return invalidEmail("invalid_recipient", "To", "To must be one email address.")
	}
	return nil
}

func newEmailSend(body []byte, idempotencyKey string, maxRetries *int) (emailSend, *EmailError) {
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	} else if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return emailSend{}, invalidEmail("invalid_idempotency_key", "IdempotencyKey", "IdempotencyKey must be 1 to 255 visible ASCII characters without spaces.")
	}
	if maxRetries != nil && *maxRetries < 0 {
		return emailSend{}, invalidEmail("invalid_max_retries", "MaxRetries", "MaxRetries must not be negative.")
	}
	return emailSend{body: body, idempotencyKey: idempotencyKey, maxRetries: maxRetries}, nil
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
			Transport: transport,
			// A redirect would move the secret key or turn the send into a GET.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		logger:  config.Logger,
		backoff: NewBackoff(time.Second, 2, 0.2, maxEmailRetryDelay).Duration,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func newDisabledEmailSender() *emailSender {
	return &emailSender{disabled: true}
}

// resolveEmailAppHost returns the app host for email requests, or "" when the
// client can't tell it. PostHog Cloud ingestion hosts don't serve the app API,
// so they map to their region's app host.
func resolveEmailAppHost(endpoint, appHost string) string {
	if appHost = strings.TrimRight(appHost, "/"); appHost != "" {
		if cloud, ok := cloudAppHosts[appHost]; ok {
			return cloud
		}
		return appHost
	}
	return cloudAppHosts[strings.TrimRight(endpoint, "/")]
}

func (s *emailSender) Send(ctx context.Context, email Email) (*SendEmailResult, error) {
	if email == nil {
		return nil, newEmailError("invalid_form", "Send needs a TemplateEmail or a CodeEmail.", nil)
	}
	send, err := email.prepare()
	if err != nil {
		return nil, err
	}
	finish, err := s.start(emailNotSent, "Sending email needs Config.SecretKey, a project secret API key with the hog_flow:write scope.")
	if err != nil {
		return nil, err
	}
	defer finish()

	maxRetries := s.maxRetries
	if send.maxRetries != nil {
		maxRetries = *send.maxRetries
	}
	header := s.header()
	header.Set("Content-Type", "application/json")
	header.Set("Idempotency-Key", send.idempotencyKey)
	reply, err := s.request(ctx, maxRetries, http.MethodPost, s.appHost+emailSendPath, header, send.body)
	if err != nil {
		return nil, err
	}
	result, err := reply.sendResult()
	if err != nil {
		return nil, err
	}
	for _, warning := range result.Warnings {
		s.logger.Warnf("PostHog email warning %s: %s", warning.Code, warning.Message)
	}
	return result, nil
}

func (s *emailSender) Get(ctx context.Context, messageID string) (*EmailStatus, error) {
	if !emailUUIDPattern.MatchString(messageID) {
		return nil, newEmailError("invalid_message_id", "messageID must be the message ID that Send returned.", nil)
	}
	finish, err := s.start(emailStatusNotRead, "Reading an email status needs Config.SecretKey, a project secret API key with the hog_flow:read or hog_flow:write scope.")
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
		// A custom endpoint is often an ingestion proxy, which must not receive the secret key.
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
	for n := 0; ; n++ {
		reply, err := s.do(ctx, method, url, header, body)
		if err == nil && !reply.accepted() {
			err = reply.error()
		}
		if err == nil {
			return reply, nil
		}
		delay, retry := s.retryDelay(err, n, maxRetries)
		if !retry {
			return emailReply{}, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return emailReply{}, err
		case <-s.ctx.Done():
			timer.Stop()
			return emailReply{}, err
		}
	}
}

func (s *emailSender) retryDelay(err *EmailError, attempt, maxRetries int) (time.Duration, bool) {
	// Retrying before a longer Retry-After would break the server's rate limit.
	if attempt >= maxRetries || !err.isRetryable() || err.RetryAfter > maxEmailRetryDelay {
		return 0, false
	}
	delay := s.backoff(attempt)
	if err.RetryAfter > delay {
		delay = err.RetryAfter
	}
	return delay, true
}

func (s *emailSender) do(ctx context.Context, method, url string, header http.Header, body []byte) (emailReply, *EmailError) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()

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
	if err != nil && reply.accepted() {
		// An accepted send may already be queued, so a failed read stays a retryable network error.
		return emailReply{}, newEmailError("network_error", "Reading PostHog's answer failed.", err)
	}
	_ = json.Unmarshal(raw, &reply.fields)
	return reply, nil
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
	return &EmailError{Name: emailErrorName(r.status, "api_error"), Code: "api_error", Message: message, StatusCode: r.status}
}

func (r emailReply) sendResult() (*SendEmailResult, *EmailError) {
	id := r.text("message_id")
	if id == "" {
		return nil, r.apiError("PostHog answered without a message ID.")
	}
	replayed, _ := r.fields["replayed"].(bool)
	result := &SendEmailResult{ID: id, Replayed: replayed, Warnings: []EmailWarning{}}
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
		result.Warnings = append(result.Warnings, EmailWarning{Code: code, Message: message, Fix: warning.text("fix")})
	}
	return result, nil
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
