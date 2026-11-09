package posthog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

// bucketServer stands in for the file storage that upload URLs point at. It
// answers each PUT with the next reply and records what it received.
type bucketServer struct {
	*httptest.Server
	mu      sync.Mutex
	replies []stubReply
	puts    []receivedRequest
}

func newBucketServer(t *testing.T, replies ...stubReply) *bucketServer {
	b := &bucketServer{replies: replies}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.puts = append(b.puts, receivedRequest{Method: r.Method, Path: r.URL.RequestURI(), Header: r.Header.Clone(), Raw: string(raw)})
		reply := stubReply{status: http.StatusOK}
		if len(b.replies) > 0 {
			reply, b.replies = b.replies[0], b.replies[1:]
		}
		b.mu.Unlock()
		for name, value := range reply.header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *bucketServer) received() []receivedRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]receivedRequest(nil), b.puts...)
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type uploadSlot map[string]interface{}

func present(content []byte) uploadSlot {
	return uploadSlot{"sha256": sha256Hex(content), "status": "present", "expires_at": "2026-12-16T10:00:00Z"}
}

func toUpload(bucket *bucketServer, content []byte) uploadSlot {
	hash := sha256Hex(content)
	return uploadSlot{
		"sha256": hash,
		"status": "upload",
		"url":    bucket.URL + "/team/2/" + hash + "?X-Amz-Signature=signed",
		"method": "PUT",
		"headers": map[string]string{
			"Content-Length":        fmt.Sprint(len(content)),
			"x-amz-checksum-sha256": "checksum-of-" + hash[:8],
		},
		"url_expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
	}
}

func uploadsAnswer(slots ...uploadSlot) stubReply {
	body, _ := json.Marshal(map[string]interface{}{"files": slots})
	return stubReply{status: http.StatusOK, body: string(body)}
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

func TestEmailsSendUploadsMissingFilesThenSendsReferences(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	logo := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe}
	bucket := newBucketServer(t)
	server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice), present(logo)), accepted("msg_1"))
	client := newEmailClient(t, server)

	result, err := client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "invoice.pdf", Content: invoice, ContentType: "application/pdf"},
		EmailAttachment{Filename: "logo.png", Content: logo, ContentID: "logo"},
	))

	require.NoError(t, err)
	require.Equal(t, "msg_1", result.ID)
	requests := server.received()
	require.Len(t, requests, 2)
	uploads, send := requests[0], requests[1]
	require.Equal(t, http.MethodPost, uploads.Method)
	require.Equal(t, "/api/projects/@current/messaging/attachments/uploads/", uploads.Path)
	require.Equal(t, "Bearer phs_test", uploads.Header.Get("Authorization"))
	require.Equal(t, map[string]interface{}{"files": []interface{}{
		map[string]interface{}{"sha256": sha256Hex(invoice), "size": float64(len(invoice))},
		map[string]interface{}{"sha256": sha256Hex(logo), "size": float64(len(logo))},
	}}, uploads.Body)

	puts := bucket.received()
	require.Len(t, puts, 1, "only the file PostHog doesn't have is uploaded")
	require.Equal(t, http.MethodPut, puts[0].Method)
	require.Equal(t, "/team/2/"+sha256Hex(invoice)+"?X-Amz-Signature=signed", puts[0].Path)
	require.Equal(t, string(invoice), puts[0].Raw)
	require.Equal(t, fmt.Sprint(len(invoice)), puts[0].Header.Get("Content-Length"))
	require.Equal(t, "checksum-of-"+sha256Hex(invoice)[:8], puts[0].Header.Get("X-Amz-Checksum-Sha256"))
	require.Empty(t, puts[0].Header.Get("Authorization"), "the upload never carries the secret key")
	require.Empty(t, puts[0].Header.Get("Transfer-Encoding"))

	require.Equal(t, "/api/projects/@current/messaging/send/email/", send.Path)
	require.Equal(t, []interface{}{
		map[string]interface{}{"sha256": sha256Hex(invoice), "filename": "invoice.pdf", "content_type": "application/pdf"},
		map[string]interface{}{"sha256": sha256Hex(logo), "filename": "logo.png", "content_id": "logo"},
	}, send.Body["attachments"], "the send names each file by its hash and carries no bytes")
}

func TestEmailsSendUploadsNothingWhenPostHogHasEveryFile(t *testing.T) {
	invoice := []byte("%PDF-1.7 terms")
	bucket := newBucketServer(t)
	server := newEmailServer(t, uploadsAnswer(present(invoice)), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "terms.pdf", Content: invoice}))

	require.NoError(t, err)
	require.Empty(t, bucket.received())
	require.Len(t, server.received(), 2)
}

func TestEmailsSendStopsWhenAnUploadFails(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t, stubReply{status: http.StatusBadRequest, body: `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>BadDigest</Code><Message>The SHA256 you specified did not match the calculated checksum.</Message></Error>`})
	server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "attachment_upload_failed", emailErr.Code)
	require.Equal(t, http.StatusBadRequest, emailErr.StatusCode)
	require.Equal(t, "Attachments[0]", emailErr.Field)
	require.Equal(t, "Uploading invoice.pdf failed: file storage answered HTTP 400, BadDigest: The SHA256 you specified did not match the calculated checksum. No email was sent.", emailErr.Message)
	require.Len(t, bucket.received(), 1, "a refused upload is not retried")
	require.Len(t, server.received(), 1, "no email is sent without its files")
}

func TestEmailsSendUploadErrorNeverHoldsTheSignedURL(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t)
	slot := toUpload(bucket, invoice)
	bucket.Close()
	server := newEmailServer(t, uploadsAnswer(slot), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), CodeEmail{
		To: "user@example.com", TemplateKey: "invoice", Subject: "Your invoice", HTML: "<p>Attached</p>",
		Attachments: []EmailAttachment{{Filename: "invoice.pdf", Content: invoice}},
		MaxRetries:  Ptr(0),
	})

	emailErr := requireEmailError(t, err)
	require.Equal(t, "network_error", emailErr.Code)
	require.Equal(t, EmailErrorServiceUnavailable, emailErr.Name)
	require.NotContains(t, fmt.Sprintf("%v %+v", err, emailErr.Unwrap()), "X-Amz-Signature")
	require.Len(t, server.received(), 1)
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
			invoice := []byte("%PDF-1.7 invoice")
			bucket := newBucketServer(t, stubReply{status: http.StatusTemporaryRedirect, header: map[string]string{"Location": location}})
			server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "invoice", Subject: "Your invoice", HTML: "<p>Attached</p>",
				Attachments: []EmailAttachment{{Filename: "invoice.pdf", Content: invoice}},
				MaxRetries:  Ptr(0),
			})

			emailErr := requireEmailError(t, err)
			require.Equal(t, "network_error", emailErr.Code)
			for cause := error(emailErr); cause != nil; cause = errors.Unwrap(cause) {
				require.NotContains(t, fmt.Sprintf("%v %+v", cause, cause), "X-Amz-Signature")
			}
			require.Len(t, bucket.received(), 1)
			require.Len(t, server.received(), 1)
		})
	}
}

func TestEmailsSendStopsWhenPostHogRefusesTheUploads(t *testing.T) {
	bucket := newBucketServer(t)
	server := newEmailServer(t, stubReply{status: http.StatusBadRequest, body: `{
		"type": "invalid_request",
		"code": "field_not_accepted",
		"detail": "This project can't send files yet, so PostHog didn't prepare the uploads.",
		"attr": "files",
		"why": "Sending files isn't available to this project yet.",
		"fix": "Send the email without files, or try again once files are available to this project."
	}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "field_not_accepted", emailErr.Code)
	require.Equal(t, EmailErrorValidation, emailErr.Name)
	require.Equal(t, "Sending files isn't available to this project yet.", emailErr.Why)
	require.Equal(t, "Attachments", emailErr.Field, "the caller never sent files, so the field is named as they know it")
	require.Len(t, server.received(), 1)
	require.Empty(t, bucket.received())
}

func TestEmailsSendReportsAMissingUploadRouteAsPostHogAnswered(t *testing.T) {
	// A PostHog without the upload route, or a project that can't send email
	// from code, answers 404. The client can't tell which, so it reports
	// PostHog's answer on the attachments instead of guessing a cause.
	bucket := newBucketServer(t)
	server := newEmailServer(t, stubReply{status: http.StatusNotFound, body: `{"type":"invalid_request","code":"not_found","detail":"Endpoint not found.","attr":null}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "not_found", emailErr.Code)
	require.Equal(t, EmailErrorNotFound, emailErr.Name)
	require.Equal(t, http.StatusNotFound, emailErr.StatusCode)
	require.Equal(t, "Attachments", emailErr.Field)
	require.Equal(t, "Endpoint not found.", emailErr.Message)
	require.Len(t, server.received(), 1, "nothing is sent")
	require.Empty(t, bucket.received())
}

func TestEmailsSendNamesTheAttachmentPostHogRefusedToTake(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusBadRequest, body: `{"type":"invalid_request","code":"message_too_large","detail":"Too large.","attr":"files__1__size"}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: []byte("png")},
		EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")},
	))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "message_too_large", emailErr.Code)
	require.Equal(t, "Attachments[1]", emailErr.Field)
}

func TestEmailsSendStopsOnAnUploadAnswerItCannotUse(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t)
	for name, slot := range map[string]uploadSlot{
		"another file's hash":   present([]byte("another file")),
		"an upload without url": {"sha256": sha256Hex(invoice), "status": "upload", "method": "PUT"},
		"an unknown status":     {"sha256": sha256Hex(invoice), "status": "scanning"},
	} {
		t.Run(name, func(t *testing.T) {
			server := newEmailServer(t, uploadsAnswer(slot), accepted("msg_1"))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

			require.Equal(t, "api_error", requireEmailError(t, err).Code)
			require.Len(t, server.received(), 1)
			require.Empty(t, bucket.received())
		})
	}
}

func TestEmailsSendCountsTheBodiesInTheMessageSize(t *testing.T) {
	scan := make([]byte, 29_100_000)
	for name, tt := range map[string]struct {
		html string
		code string
	}{
		"beside a short body it fits":         {html: "<p>Your scan</p>", code: "field_not_accepted"},
		"beside a 100 KB body it's too large": {html: "<p>" + strings.Repeat("x", 100_000) + "</p>", code: "message_too_large"},
	} {
		t.Run(name, func(t *testing.T) {
			server := newEmailServer(t, failed(http.StatusBadRequest, "field_not_accepted"))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "scan", Subject: "Your scan", HTML: tt.html,
				Attachments: []EmailAttachment{{Filename: "scan.pdf", Content: scan}},
			})

			require.Equal(t, tt.code, requireEmailError(t, err).Code)
		})
	}
}

func TestEmailsSendRetriesAnUploadWhoseAnswerBrokeOff(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	var mu sync.Mutex
	var uploads []string
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		uploads = append(uploads, string(raw))
		first := len(uploads) == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<Error>"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(bucket.Close)
	slot := uploadSlot{"sha256": sha256Hex(invoice), "status": "upload", "url": bucket.URL + "/team/2/file", "method": "PUT", "headers": map[string]string{}}
	server := newEmailServer(t, uploadsAnswer(slot), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	require.Equal(t, []string{string(invoice), string(invoice)}, uploads)
	require.Len(t, server.received(), 2)
}

func TestEmailsSendKeepsStoragesWaitWhenItsAnswerBrokeOff(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	var mu sync.Mutex
	puts := 0
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		puts++
		mu.Unlock()
		w.Header().Set("Retry-After", "120")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<Error>"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(bucket.Close)
	slot := uploadSlot{"sha256": sha256Hex(invoice), "status": "upload", "url": bucket.URL + "/team/2/file", "method": "PUT", "headers": map[string]string{}}
	server := newEmailServer(t, uploadsAnswer(slot))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "network_error", emailErr.Code)
	require.Equal(t, 2*time.Minute, emailErr.RetryAfter)
	mu.Lock()
	require.Equal(t, 1, puts, "a wait longer than the client retries goes back to the caller")
	mu.Unlock()
	require.Len(t, server.received(), 1)
}

func TestEmailsSendAsksAgainForAnUploadURLThatExpired(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t)
	expired := toUpload(bucket, invoice)
	expired["url"] = bucket.URL + "/expired"
	expired["url_expires_at"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	fresh := toUpload(bucket, invoice)
	server := newEmailServer(t, uploadsAnswer(expired), uploadsAnswer(fresh), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	requests := server.received()
	require.Len(t, requests, 3)
	require.Equal(t, requests[0].Body, requests[1].Body, "the client asks again for the same file")
	puts := bucket.received()
	require.Len(t, puts, 1)
	require.NotContains(t, puts[0].Path, "/expired")
}

func TestEmailsSendUploadsAFileAttachedTwiceOnce(t *testing.T) {
	logo := []byte{0x89, 'P', 'N', 'G'}
	bucket := newBucketServer(t)
	server := newEmailServer(t, uploadsAnswer(toUpload(bucket, logo), toUpload(bucket, logo)), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(
		EmailAttachment{Filename: "logo.png", Content: logo, ContentID: "logo"},
		EmailAttachment{Filename: "logo-copy.png", Content: logo},
	))

	require.NoError(t, err)
	require.Len(t, bucket.received(), 1)
	require.Len(t, server.received()[1].Body["attachments"], 2)
}

func TestEmailsSendRetriesWhatFileStorageAsksToRetry(t *testing.T) {
	for name, refusal := range map[string]stubReply{
		"500":            {status: http.StatusInternalServerError, body: `<Error><Code>InternalError</Code></Error>`},
		"502":            {status: http.StatusBadGateway},
		"503":            {status: http.StatusServiceUnavailable, body: `<Error><Code>SlowDown</Code></Error>`},
		"504":            {status: http.StatusGatewayTimeout},
		"429":            {status: http.StatusTooManyRequests},
		"RequestTimeout": {status: http.StatusBadRequest, body: `<Error><Code>RequestTimeout</Code><Message>Your socket connection to the server was not read from or written to within the timeout period.</Message></Error>`},
	} {
		t.Run(name, func(t *testing.T) {
			invoice := []byte("%PDF-1.7 invoice")
			bucket := newBucketServer(t, refusal)
			server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)), accepted("msg_1"))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

			require.NoError(t, err)
			puts := bucket.received()
			require.Len(t, puts, 2)
			require.Equal(t, string(invoice), puts[1].Raw, "the retry uploads the same bytes")
			require.Len(t, server.received(), 2)
		})
	}
}

func TestEmailsSendNamesAStorageRefusalByWhatItAsks(t *testing.T) {
	for name, tt := range map[string]struct {
		refusal stubReply
		want    EmailErrorName
	}{
		"throttled":    {stubReply{status: http.StatusTooManyRequests, body: `<Error><Code>SlowDown</Code></Error>`}, EmailErrorRateLimitExceeded},
		"timed out":    {stubReply{status: http.StatusBadRequest, body: `<Error><Code>RequestTimeout</Code></Error>`}, EmailErrorServiceUnavailable},
		"unavailable":  {stubReply{status: http.StatusServiceUnavailable, body: `<Error><Code>SlowDown</Code></Error>`}, EmailErrorServiceUnavailable},
		"wrong digest": {stubReply{status: http.StatusBadRequest, body: `<Error><Code>BadDigest</Code></Error>`}, EmailErrorApplication},
	} {
		t.Run(name, func(t *testing.T) {
			invoice := []byte("%PDF-1.7 invoice")
			bucket := newBucketServer(t, tt.refusal)
			server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)))
			client := newEmailClient(t, server)

			_, err := client.Emails().Send(context.Background(), CodeEmail{
				To: "user@example.com", TemplateKey: "invoice", Subject: "Your invoice", HTML: "<p>Attached</p>",
				Attachments: []EmailAttachment{{Filename: "invoice.pdf", Content: invoice}},
				MaxRetries:  Ptr(0),
			})

			emailErr := requireEmailError(t, err)
			require.Equal(t, "attachment_upload_failed", emailErr.Code)
			require.Equal(t, tt.want, emailErr.Name)
		})
	}
}

func TestEmailsSendWaitsAsLongAsFileStorageAsks(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t, stubReply{status: http.StatusServiceUnavailable, header: map[string]string{"Retry-After": "120"}, body: `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`})
	server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "attachment_upload_failed", emailErr.Code)
	require.Equal(t, 2*time.Minute, emailErr.RetryAfter)
	require.Len(t, bucket.received(), 1, "a wait longer than the client retries goes back to the caller")
	require.Len(t, server.received(), 1)
}

func TestEmailsSendDoesNotRetryTheDailyUploadQuota(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusTooManyRequests, body: `{"type":"throttled_error","code":"upload_quota_exceeded","detail":"This project uploaded as many file bytes as it can today."}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: []byte("%PDF")}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "upload_quota_exceeded", emailErr.Code)
	require.Equal(t, EmailErrorDailyQuotaExceeded, emailErr.Name)
	require.Len(t, server.received(), 1)
}

func TestEmailsSendSkipsAnExpiredUploadPostHogNowHas(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t)
	expired := toUpload(bucket, invoice)
	expired["url_expires_at"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	server := newEmailServer(t, uploadsAnswer(expired), uploadsAnswer(present(invoice)), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	require.Empty(t, bucket.received())
	require.Len(t, server.received(), 3)
}

func TestEmailsSendKeepsPostHogsFieldWhenAskingAgainFails(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t)
	expired := toUpload(bucket, invoice)
	expired["url_expires_at"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	server := newEmailServer(t, uploadsAnswer(expired), stubReply{status: http.StatusTooManyRequests, body: `{"type":"throttled_error","code":"upload_quota_exceeded","detail":"This project uploaded as many file bytes as it can today."}`})
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	emailErr := requireEmailError(t, err)
	require.Equal(t, "upload_quota_exceeded", emailErr.Code)
	require.Equal(t, EmailErrorDailyQuotaExceeded, emailErr.Name)
	require.Empty(t, emailErr.Field)
	require.Empty(t, bucket.received())
}

func TestUploadTargetExpiresFifteenMinutesAfterTheAnswerByTheLocalClock(t *testing.T) {
	file := attachment{SHA256: sha256Hex([]byte("invoice")), Filename: "invoice.pdf", content: []byte("invoice")}
	answer := func(urlExpiresAt time.Time) emailReply {
		return emailReply{status: http.StatusOK, fields: map[string]interface{}{"files": []interface{}{map[string]interface{}{
			"sha256": file.SHA256, "status": "upload", "url": "https://storage.example/invoice", "method": "PUT",
			"url_expires_at": urlExpiresAt.UTC().Format(time.RFC3339Nano),
		}}}}
	}

	// PostHog's clock is ahead of the client's, so its deadline reads an hour
	// away. By the client's clock the URL lives 15 minutes, and is asked for
	// again in its last minute.
	targets, err := answer(time.Now().Add(time.Hour)).uploadTargets([]attachment{file}, time.Now().Add(-14*time.Minute-time.Second))
	require.Nil(t, err)
	require.True(t, targets[0].expired())

	targets, err = answer(time.Now().Add(time.Hour)).uploadTargets([]attachment{file}, time.Now().Add(-13*time.Minute))
	require.Nil(t, err)
	require.False(t, targets[0].expired())

	// An earlier deadline from PostHog still wins.
	targets, err = answer(time.Now().Add(30*time.Second)).uploadTargets([]attachment{file}, time.Now())
	require.Nil(t, err)
	require.True(t, targets[0].expired())
}
