package posthog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testClock is a clock the test moves forward by hand.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// attachmentsWorld is PostHog and its file storage, with a client that sends
// email through them.
type attachmentsWorld struct {
	storage *fakeStorage
	posthog *fakePostHog
	client  Client
}

func newAttachmentsWorld(t *testing.T, faults ...storageFault) attachmentsWorld {
	storage := newFakeStorage(t, time.Now, faults...)
	posthog := newFakePostHog(t, storage)
	return attachmentsWorld{storage: storage, posthog: posthog, client: newEmailClientFor(t, posthog.URL)}
}

func invoiceWith(attachments ...EmailAttachment) CodeEmail {
	return CodeEmail{
		To:          "user@example.com",
		TemplateKey: "invoice",
		Subject:     "Your invoice",
		HTML:        `<p>Attached</p><img src="cid:logo">`,
		Attachments: attachments,
	}
}

func withoutRetries(email CodeEmail) CodeEmail {
	email.MaxRetries = Ptr(0)
	return email
}

func storageRefusal(status int, code string) storageFault {
	return storageFault{stubReply: stubReply{status: status, body: `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>` + code + `</Code><Message>Storage said no.</Message></Error>`}}
}

func TestEmailsSendDeliversAttachments(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	logo := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe}
	world := newAttachmentsWorld(t)
	world.storage.put(logo)

	result, err := world.client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "invoice.pdf", Content: invoice, ContentType: "application/pdf"},
		EmailAttachment{Filename: "logo.png", Content: logo, ContentID: "logo"},
	))

	require.NoError(t, err)
	require.Equal(t, "msg_1", result.ID)
	require.Equal(t, []deliveredEmail{{
		To: "user@example.com", Subject: "Your invoice",
		Attachments: []deliveredAttachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Content: invoice},
			{Filename: "logo.png", ContentID: "logo", Content: logo},
		},
	}}, world.posthog.deliveredEmails())
	require.Equal(t, invoice, world.storage.stored(sha256Hex(invoice)))
}

func TestEmailsSendDeliversFilesPostHogHasWithoutStorage(t *testing.T) {
	terms := []byte("%PDF-1.7 terms")
	world := newAttachmentsWorld(t)
	world.storage.put(terms)
	world.storage.Close()

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "terms.pdf", Content: terms}))

	require.NoError(t, err)
	require.Equal(t, terms, world.posthog.deliveredEmails()[0].Attachments[0].Content)
}

func TestEmailsSendDeliversAFileAttachedTwice(t *testing.T) {
	// Storage throttles a second upload of the same bytes. The file is
	// already there, so the email still goes out.
	logo := []byte{0x89, 'P', 'N', 'G'}
	world := newAttachmentsWorld(t, storageFault{}, storageRefusal(http.StatusTooManyRequests, "SlowDown"))

	_, err := world.client.Emails().Send(context.Background(), withoutRetries(invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: logo, ContentID: "logo"},
		EmailAttachment{Filename: "logo-copy.png", Content: logo},
	)))

	require.NoError(t, err)
	require.Equal(t, []deliveredAttachment{
		{Filename: "logo.png", ContentID: "logo", Content: logo},
		{Filename: "logo-copy.png", Content: logo},
	}, world.posthog.deliveredEmails()[0].Attachments)
}

func TestEmailsSendStopsWhenAnUploadFails(t *testing.T) {
	// The fault is used once, so a retry would upload the file and send.
	invoice := []byte("%PDF-1.7 invoice")
	world := newAttachmentsWorld(t, storageFault{stubReply: stubReply{status: http.StatusBadRequest, body: `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>BadDigest</Code><Message>The SHA256 you specified did not match the calculated checksum.</Message></Error>`}})

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: []byte("png")},
		EmailAttachment{Filename: "invoice.pdf", Content: invoice},
	))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "attachment_upload_failed", emailErr.Code)
	require.Equal(t, EmailErrorApplication, emailErr.Name)
	require.Equal(t, http.StatusBadRequest, emailErr.StatusCode)
	require.Equal(t, "Attachments[0]", emailErr.Field)
	require.Equal(t, "Uploading logo.png failed: file storage answered HTTP 400, BadDigest: The SHA256 you specified did not match the calculated checksum. No email was sent.", emailErr.Message)
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendStopsWhenStorageIsUnreachable(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	world := newAttachmentsWorld(t)
	world.storage.Close()

	_, err := world.client.Emails().Send(context.Background(), withoutRetries(invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice})))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "network_error", emailErr.Code)
	require.Equal(t, EmailErrorServiceUnavailable, emailErr.Name)
	require.Equal(t, "Attachments[0]", emailErr.Field)
	require.Equal(t, "Uploading invoice.pdf failed. No email was sent.", emailErr.Message)
	for cause := error(emailErr); cause != nil; cause = errors.Unwrap(cause) {
		require.NotContains(t, fmt.Sprintf("%v %+v", cause, cause), "X-Amz-Signature")
	}
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendUploadErrorNeverHoldsARedirectsSignedURL(t *testing.T) {
	// Go reads a redirect's Location before any redirect policy runs, and
	// quotes a Location it can't parse in its error. Storage redirects keep
	// the request's signature, so that text must not reach the caller.
	for name, location := range map[string]string{
		"absolute": "http://storage.example/team/2/%zz?X-Amz-Signature=secret",
		"relative": "/team/2/%zz?X-Amz-Signature=secret",
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t, storageFault{stubReply: stubReply{status: http.StatusTemporaryRedirect, header: map[string]string{"Location": location}}})

			_, err := world.client.Emails().Send(context.Background(), withoutRetries(invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")})))

			emailErr := requireEmailError(t, err)
			require.Equal(t, "network_error", emailErr.Code)
			for cause := error(emailErr); cause != nil; cause = errors.Unwrap(cause) {
				require.NotContains(t, fmt.Sprintf("%v %+v", cause, cause), "X-Amz-Signature")
			}
			require.Empty(t, world.posthog.deliveredEmails())
		})
	}
}

func TestEmailsSendNeverFollowsAStorageRedirect(t *testing.T) {
	// Following a 307 would re-send the file to wherever storage points, even
	// over http, and report success. A signed upload never works through a
	// redirect, so a redirect fails the attachment.
	var elsewhere int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhere++
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	world := newAttachmentsWorld(t, storageFault{stubReply: stubReply{status: http.StatusTemporaryRedirect, header: map[string]string{"Location": other.URL + "/team/2/invoice"}}})

	_, err := world.client.Emails().Send(context.Background(), withoutRetries(invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")})))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "attachment_upload_failed", emailErr.Code)
	require.Equal(t, http.StatusTemporaryRedirect, emailErr.StatusCode)
	require.Equal(t, "Attachments[0]", emailErr.Field)
	require.Equal(t, "Uploading invoice.pdf failed: file storage answered HTTP 307. No email was sent.", emailErr.Message)
	for cause := error(emailErr); cause != nil; cause = errors.Unwrap(cause) {
		require.NotContains(t, fmt.Sprintf("%v %+v", cause, cause), other.URL)
	}
	mu.Lock()
	require.Zero(t, elsewhere, "the file went to the redirect's target")
	mu.Unlock()
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendStopsWhenPostHogRefusesTheUploads(t *testing.T) {
	world := newAttachmentsWorld(t)
	world.posthog.uploadFaults = map[int]stubReply{0: {status: http.StatusBadRequest, body: `{
		"type": "invalid_request",
		"code": "field_not_accepted",
		"detail": "This project can't send files yet, so PostHog didn't prepare the uploads.",
		"attr": "files",
		"why": "Sending files isn't available to this project yet.",
		"fix": "Send the email without files, or try again once files are available to this project."
	}`}}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "field_not_accepted", emailErr.Code)
	require.Equal(t, EmailErrorValidation, emailErr.Name)
	require.Equal(t, "Sending files isn't available to this project yet.", emailErr.Why)
	require.Equal(t, "Attachments", emailErr.Field, "the caller never sent files, so the field is named as they know it")
	require.Empty(t, world.posthog.deliveredEmails())
	require.True(t, world.storage.isEmpty())
}

func TestEmailsSendReportsAMissingUploadRouteAsPostHogAnswered(t *testing.T) {
	// A PostHog without the upload route, or a project that can't send email
	// from code, answers 404. The client can't tell which, so it reports
	// PostHog's answer on the attachments instead of guessing a cause.
	world := newAttachmentsWorld(t)
	world.posthog.withoutUploadRoute = true

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "not_found", emailErr.Code)
	require.Equal(t, EmailErrorNotFound, emailErr.Name)
	require.Equal(t, http.StatusNotFound, emailErr.StatusCode)
	require.Equal(t, "Attachments", emailErr.Field)
	require.Equal(t, "Endpoint not found.", emailErr.Message)
	require.Empty(t, world.posthog.deliveredEmails())
	require.True(t, world.storage.isEmpty())
}

func TestEmailsSendNamesTheAttachmentPostHogRefusedToTake(t *testing.T) {
	world := newAttachmentsWorld(t)
	world.posthog.uploadFaults = map[int]stubReply{0: {status: http.StatusBadRequest, body: `{"type":"invalid_request","code":"message_too_large","detail":"Too large.","attr":"files__1__size"}`}}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: []byte("png")},
		EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")},
	))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "message_too_large", emailErr.Code)
	require.Equal(t, "Attachments[1]", emailErr.Field)
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendStopsOnAnUploadAnswerItCannotUse(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	for name, slot := range map[string]string{
		"another file's hash":   `{"sha256":"` + sha256Hex([]byte("another file")) + `","status":"present","expires_at":"2026-12-16T10:00:00Z"}`,
		"an upload without url": `{"sha256":"` + sha256Hex(invoice) + `","status":"upload","method":"PUT"}`,
		"an unknown status":     `{"sha256":"` + sha256Hex(invoice) + `","status":"scanning"}`,
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t)
			world.posthog.uploadFaults = map[int]stubReply{0: {status: http.StatusOK, body: `{"files":[` + slot + `]}`}}

			_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

			require.Equal(t, "api_error", requireEmailError(t, err).Code)
			require.Empty(t, world.posthog.deliveredEmails())
			require.True(t, world.storage.isEmpty())
		})
	}
}

func TestEmailsSendCountsTheBodiesInTheMessageSize(t *testing.T) {
	scan := make([]byte, 29_100_000)
	for name, tt := range map[string]struct {
		html string
		fits bool
	}{
		"beside a short body it fits":         {html: "<p>Your scan</p>", fits: true},
		"beside a 100 KB body it's too large": {html: "<p>" + strings.Repeat("x", 100_000) + "</p>"},
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t)

			_, err := world.client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "scan", Subject: "Your scan", HTML: tt.html,
				Attachments: []EmailAttachment{{Filename: "scan.pdf", Content: scan}},
			})

			requireFitsTheMessageSize(t, world, err, tt.fits)
		})
	}
}

func TestEmailsSendCountsTheSubjectInTheMessageSize(t *testing.T) {
	// PostHog counts a subject as 75 bytes per 42 bytes of UTF-8, the encoded
	// words it folds into. With a one-byte text body, this file leaves room
	// for one encoded word, not two.
	scan := make([]byte, 29_182_800)
	for name, tt := range map[string]struct {
		subject string
		fits    bool
	}{
		"a 7-byte subject fits":         {subject: "Receipt", fits: true},
		"a 50-byte subject is too much": {subject: "Ihre Bestellbestätigung Nr. 12345 vom 9. November"},
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t)

			_, err := world.client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "scan", Subject: tt.subject, Text: "a",
				Attachments: []EmailAttachment{{Filename: "scan.pdf", Content: scan}},
			})

			requireFitsTheMessageSize(t, world, err, tt.fits)
		})
	}
}

func TestEmailsSendCountsTheTextPostHogDerivesFromTheHTML(t *testing.T) {
	// Without Text, PostHog derives the plain text from the HTML and counts it
	// too. The derived text is never longer than the HTML, so the client
	// counts the HTML twice: this file and HTML fit beside a one-byte text,
	// and not alone.
	scan := make([]byte, 29_082_000)
	html := "<p>" + strings.Repeat("x", 99_993) + "</p>"
	for name, tt := range map[string]struct {
		text string
		fits bool
	}{
		"with its own short text it fits": {text: "a", fits: true},
		"without text it's too large":     {},
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t)

			_, err := world.client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "scan", Subject: "Your scan", HTML: html, Text: tt.text,
				Attachments: []EmailAttachment{{Filename: "scan.pdf", Content: scan}},
			})

			requireFitsTheMessageSize(t, world, err, tt.fits)
		})
	}
}

func requireFitsTheMessageSize(t *testing.T, world attachmentsWorld, err error, fits bool) {
	t.Helper()
	if fits {
		require.NoError(t, err)
		require.Len(t, world.posthog.deliveredEmails(), 1)
		return
	}
	emailErr := requireEmailError(t, err)
	require.Equal(t, "message_too_large", emailErr.Code)
	require.Equal(t, "Attachments", emailErr.Field)
	require.True(t, world.storage.isEmpty(), "nothing is uploaded for an email PostHog would refuse")
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendUploadsOverHTTPOnlyWhenAllowed(t *testing.T) {
	// PostHog over HTTPS names a plain-HTTP storage URL, as misconfigured
	// self-hosted storage would. The file and its signed URL would travel
	// unencrypted, so the client stops unless AllowInsecureAppHost allows
	// plain HTTP, as it does for local development.
	storage := newFakeStorage(t, time.Now)
	posthog := newFakePostHogOverTLS(t, storage)
	client := newEmailClientFor(t, posthog.URL, func(c *Config) {
		c.AllowInsecureAppHost = false
		c.Transport = posthog.Client().Transport
	})

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "api_error", emailErr.Code)
	require.Equal(t, "Attachments[0]", emailErr.Field)
	require.True(t, storage.isEmpty())
	require.Empty(t, posthog.deliveredEmails())
}

func TestEmailsSendRetriesWhatFileStorageAsksToRetry(t *testing.T) {
	for name, fault := range map[string]storageFault{
		"500":                 storageRefusal(http.StatusInternalServerError, "InternalError"),
		"502":                 {stubReply: stubReply{status: http.StatusBadGateway}},
		"503":                 storageRefusal(http.StatusServiceUnavailable, "SlowDown"),
		"504":                 {stubReply: stubReply{status: http.StatusGatewayTimeout}},
		"429":                 {stubReply: stubReply{status: http.StatusTooManyRequests}},
		"RequestTimeout":      storageRefusal(http.StatusBadRequest, "RequestTimeout"),
		"an answer broke off": {stubReply: stubReply{status: http.StatusOK}, breakOff: true},
	} {
		t.Run(name, func(t *testing.T) {
			invoice := []byte("%PDF-1.7 invoice")
			world := newAttachmentsWorld(t, fault)

			_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

			require.NoError(t, err)
			require.Equal(t, invoice, world.storage.stored(sha256Hex(invoice)))
			require.Equal(t, invoice, world.posthog.deliveredEmails()[0].Attachments[0].Content)
		})
	}
}

func TestEmailsSendNamesAStorageRefusalByWhatItAsks(t *testing.T) {
	for name, tt := range map[string]struct {
		refusal storageFault
		want    EmailErrorName
	}{
		"throttled":    {storageRefusal(http.StatusTooManyRequests, "SlowDown"), EmailErrorRateLimitExceeded},
		"timed out":    {storageRefusal(http.StatusBadRequest, "RequestTimeout"), EmailErrorServiceUnavailable},
		"unavailable":  {storageRefusal(http.StatusServiceUnavailable, "SlowDown"), EmailErrorServiceUnavailable},
		"wrong digest": {storageRefusal(http.StatusBadRequest, "BadDigest"), EmailErrorApplication},
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t, tt.refusal)

			_, err := world.client.Emails().Send(context.Background(), withoutRetries(invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")})))

			emailErr := requireEmailError(t, err)
			require.Equal(t, "attachment_upload_failed", emailErr.Code)
			require.Equal(t, tt.want, emailErr.Name)
			require.Empty(t, world.posthog.deliveredEmails())
		})
	}
}

func TestEmailsSendHandsBackAWaitLongerThanItRetries(t *testing.T) {
	// Storage asks for two minutes, longer than the client waits, so the
	// caller gets the wait instead of a blocked send.
	for name, fault := range map[string]storageFault{
		"a refusal":                {stubReply: stubReply{status: http.StatusServiceUnavailable, header: map[string]string{"Retry-After": "120"}, body: `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`}},
		"an answer that broke off": {stubReply: stubReply{status: http.StatusServiceUnavailable, header: map[string]string{"Retry-After": "120"}}, breakOff: true},
	} {
		t.Run(name, func(t *testing.T) {
			world := newAttachmentsWorld(t, fault)

			_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

			emailErr := requireEmailError(t, err)
			require.Equal(t, 2*time.Minute, emailErr.RetryAfter)
			require.Empty(t, world.posthog.deliveredEmails())
		})
	}
}

func TestEmailsSendDoesNotRetryTheDailyUploadQuota(t *testing.T) {
	// The quota answers once, so a retry would upload the file and send.
	world := newAttachmentsWorld(t)
	world.posthog.uploadFaults = map[int]stubReply{0: dailyUploadQuotaExceeded()}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "upload_quota_exceeded", emailErr.Code)
	require.Equal(t, EmailErrorDailyQuotaExceeded, emailErr.Name)
	require.Empty(t, world.posthog.deliveredEmails())
}

func dailyUploadQuotaExceeded() stubReply {
	return stubReply{status: http.StatusTooManyRequests, body: `{"type":"throttled_error","code":"upload_quota_exceeded","detail":"This project uploaded as many file bytes as it can today."}`}
}

func TestEmailsSendAsksAgainForAnUploadURLThatExpired(t *testing.T) {
	// PostHog's first answer names a URL that has already expired.
	invoice := []byte("%PDF-1.7 invoice")
	world := newAttachmentsWorld(t)
	world.posthog.urlLifetimes = []time.Duration{-time.Second}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	require.Equal(t, invoice, world.storage.stored(sha256Hex(invoice)))
	require.Len(t, world.posthog.deliveredEmails(), 1)
}

func TestEmailsSendTimesAnUploadURLByItsOwnClock(t *testing.T) {
	// The client's clock runs an hour behind PostHog's and storage's, so
	// PostHog's deadline reads an hour later than it is. The first upload
	// takes 16 minutes, past the second URL's life, and the client must
	// notice by timing the URL itself.
	invoice, logo := []byte("%PDF-1.7 invoice"), []byte("logo")
	clock := &testClock{at: time.Now()}
	storage := newFakeStorage(t, clock.now)
	posthog := newFakePostHog(t, storage)
	firstUpload := sync.Once{}
	storage.afterStore = func() { firstUpload.Do(func() { clock.advance(16 * time.Minute) }) }
	client := newEmailClientFor(t, posthog.URL, func(c *Config) {
		c.now = func() time.Time { return clock.now().Add(-time.Hour) }
	})

	_, err := client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "invoice.pdf", Content: invoice},
		EmailAttachment{Filename: "logo.png", Content: logo},
	))

	require.NoError(t, err)
	require.Equal(t, logo, storage.stored(sha256Hex(logo)))
	require.Len(t, posthog.deliveredEmails(), 1)
}

func TestEmailsSendSkipsAnExpiredUploadPostHogNowHas(t *testing.T) {
	// The URL expired, and by the time the client asks again another send
	// uploaded the same file, so storage is no longer needed.
	invoice := []byte("%PDF-1.7 invoice")
	world := newAttachmentsWorld(t)
	world.posthog.urlLifetimes = []time.Duration{-time.Second}
	world.posthog.beforeUploadAnswer = func(n int) {
		if n == 1 {
			world.storage.put(invoice)
			world.storage.Close()
		}
	}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	require.Equal(t, invoice, world.posthog.deliveredEmails()[0].Attachments[0].Content)
}

func TestEmailsSendNamesTheAttachmentWhoseURLPostHogWouldNotRenew(t *testing.T) {
	world := newAttachmentsWorld(t)
	world.posthog.urlLifetimes = []time.Duration{-time.Second}
	world.storage.put([]byte("png"))
	world.posthog.uploadFaults = map[int]stubReply{1: {status: http.StatusBadRequest, body: `{"type":"invalid_request","code":"message_too_large","detail":"Too large.","attr":"files__0__size"}`}}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: []byte("png")},
		EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF-1.7 invoice")},
	))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "message_too_large", emailErr.Code)
	require.Equal(t, "Attachments[1]", emailErr.Field, "the second ask lists one file, the second attachment")
	require.Empty(t, world.posthog.deliveredEmails())
}

func TestEmailsSendKeepsPostHogsFieldWhenAskingAgainFails(t *testing.T) {
	world := newAttachmentsWorld(t)
	world.posthog.urlLifetimes = []time.Duration{-time.Second}
	world.posthog.uploadFaults = map[int]stubReply{1: dailyUploadQuotaExceeded()}

	_, err := world.client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "upload_quota_exceeded", emailErr.Code)
	require.Equal(t, EmailErrorDailyQuotaExceeded, emailErr.Name)
	require.Empty(t, emailErr.Field)
	require.Empty(t, world.posthog.deliveredEmails())
}
