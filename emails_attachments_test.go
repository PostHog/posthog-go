package posthog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
		"url_expires_at": "2026-11-09T10:15:00Z",
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

func TestEmailsSendRetriesAnUploadTheStorageCouldNotTake(t *testing.T) {
	invoice := []byte("%PDF-1.7 invoice")
	bucket := newBucketServer(t, stubReply{status: http.StatusServiceUnavailable, body: `<Error><Code>SlowDown</Code></Error>`})
	server := newEmailServer(t, uploadsAnswer(toUpload(bucket, invoice)), accepted("msg_1"))
	client := newEmailClient(t, server)

	_, err := client.Emails().Send(context.Background(), invoiceWith(EmailAttachment{Filename: "invoice.pdf", Content: invoice}))

	require.NoError(t, err)
	puts := bucket.received()
	require.Len(t, puts, 2)
	require.Equal(t, string(invoice), puts[1].Raw, "the retry uploads the same bytes")
	require.Len(t, server.received(), 2)
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
	require.Len(t, server.received(), 1)
	require.Empty(t, bucket.received())
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
