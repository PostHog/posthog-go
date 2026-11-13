package posthog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	emailUploadsPath = "/api/projects/@current/messaging/attachments/uploads/"
	// maxEmailAttachments and maxEmailMessageBytes are PostHog's limits on one
	// email: 10 files, and 40 MB for the message after Base64 encoding.
	maxEmailAttachments  = 10
	maxEmailMessageBytes = 40_000_000
	// emailHeadersAndMIMEBytes is what PostHog reserves for the message's
	// headers and MIME boundaries when it estimates its size. It counts the
	// subject apart, as encoded words of 75 bytes per 42 bytes of UTF-8.
	emailHeadersAndMIMEBytes   = 64 * 1024
	subjectBytesPerEncodedWord = 42
	encodedWordBytes           = 75
	// emailUploadTimeout is how long an upload URL accepts the upload.
	emailUploadTimeout = 15 * time.Minute
	// uploadURLMargin is how long before its expiry an upload URL is asked for
	// again, so a PUT never starts on a URL that expires on its way.
	uploadURLMargin      = time.Minute
	maxBucketAnswerBytes = 64 << 10
)

// attachment is an EmailAttachment named by the SHA-256 of its content. A
// send carries it as a reference: its content goes only to file storage.
type attachment struct {
	SHA256      string `json:"sha256"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
	content     []byte
}

type uploadRequestFile struct {
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// uploadTarget is where PostHog asked the client to upload one file.
type uploadTarget struct {
	url       string
	method    string
	headers   map[string]string
	expiresAt time.Time
}

// expired says whether the URL may no longer accept an upload that starts now.
func (t uploadTarget) expired(now time.Time) bool {
	return !now.Before(t.expiresAt.Add(-uploadURLMargin))
}

func checkAttachments(attachments []EmailAttachment, subject, html, text string) ([]attachment, *EmailError) {
	if len(attachments) > maxEmailAttachments {
		return nil, invalidEmail("too_many_attachments", "Attachments", "An email carries at most 10 attachments.")
	}
	files := make([]attachment, 0, len(attachments))
	sizes := make([]int, 0, len(attachments)+2)
	for i, file := range attachments {
		field := "Attachments[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(file.Filename) == "" {
			return nil, invalidEmail("invalid_attachment", field+".Filename", "An attachment needs a Filename, such as \"invoice.pdf\".")
		}
		if len(file.Content) == 0 {
			return nil, invalidEmail("invalid_attachment", field+".Content", "An attachment needs Content, the file's bytes.")
		}
		sum := sha256.Sum256(file.Content)
		files = append(files, attachment{
			SHA256:      hex.EncodeToString(sum[:]),
			Filename:    file.Filename,
			ContentType: file.ContentType,
			ContentID:   file.ContentID,
			content:     file.Content,
		})
		sizes = append(sizes, len(file.Content))
	}
	if text == "" {
		// PostHog derives the text from the HTML and counts it too. The
		// derived text is never longer than the HTML.
		text = html
	}
	sizes = append(sizes, len(html), len(text))
	if len(files) > 0 && estimateMessageBytes(sizes, len(subject)) > maxEmailMessageBytes {
		return nil, invalidEmail("message_too_large", "Attachments", "The email would be over 40 MB after Base64 encoding, PostHog's limit. Send fewer or smaller files, about 29 MB in total, or link to large files from the body.")
	}
	return files, nil
}

// estimateMessageBytes is PostHog's estimate of a message whose files and
// bodies have these sizes: each part Base64 encoded in 76-character lines,
// plus the encoded subject and room for headers and MIME boundaries.
func estimateMessageBytes(partSizes []int, subjectBytes int) int64 {
	encodedWords := (int64(subjectBytes) + subjectBytesPerEncodedWord - 1) / subjectBytesPerEncodedWord
	total := int64(emailHeadersAndMIMEBytes) + encodedWords*encodedWordBytes
	for _, size := range partSizes {
		encoded := (int64(size) + 2) / 3 * 4
		total += (encoded*78 + 75) / 76
	}
	return total
}

// uploadAttachments asks PostHog which files it lacks and uploads each one
// once, so a send can name them by their hashes. It returns at the first
// failure.
func (s *emailSender) uploadAttachments(ctx context.Context, maxRetries int, files []attachment) *EmailError {
	targets, err := s.uploadTargets(ctx, maxRetries, files, 0)
	if err != nil {
		return err
	}
	uploaded := map[string]bool{}
	for i, target := range targets {
		file := files[i]
		if target == nil || uploaded[file.SHA256] {
			continue
		}
		if err := s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
			if target.expired(s.now()) {
				fresh, err := s.uploadTargets(ctx, 0, []attachment{file}, i)
				if err != nil {
					return err
				}
				if target = fresh[0]; target == nil {
					return nil
				}
			}
			if err := s.upload(ctx, file, *target); err != nil {
				err.Field = "Attachments[" + strconv.Itoa(i) + "]"
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		uploaded[file.SHA256] = true
	}
	return nil
}

// uploadTargets asks PostHog where to upload each file: per file, in request
// order, nil when PostHog has the file and a target otherwise. The files are
// the attachments from index first on, so a refusal names the right one.
func (s *emailSender) uploadTargets(ctx context.Context, maxRetries int, files []attachment, first int) ([]*uploadTarget, *EmailError) {
	requested := make([]uploadRequestFile, 0, len(files))
	for _, file := range files {
		requested = append(requested, uploadRequestFile{SHA256: file.SHA256, Size: len(file.content)})
	}
	body, err := encodeEmailBody(map[string]interface{}{"files": requested})
	if err != nil {
		return nil, newEmailError("unexpected_error", "The client could not encode the upload request.", err)
	}
	header := s.header()
	header.Set("Content-Type", "application/json")
	reply, emailErr := s.request(ctx, maxRetries, http.MethodPost, s.appHost+emailUploadsPath, header, body)
	if emailErr != nil {
		emailErr.Field = attachmentsField(emailErr, first)
		return nil, emailErr
	}
	targets, emailErr := reply.uploadTargets(files, s.now())
	if emailErr != nil {
		return nil, emailErr
	}
	for i, target := range targets {
		if target != nil && !s.allowInsecureUploads && !strings.HasPrefix(strings.ToLower(target.url), "https://") {
			emailErr := reply.apiError("PostHog named an upload URL that isn't https, so " + files[i].Filename + " would travel unencrypted. Fix the file storage's URL, or set AllowInsecureAppHost for local development. No email was sent.")
			emailErr.Field = "Attachments[" + strconv.Itoa(first+i) + "]"
			return nil, emailErr
		}
	}
	return targets, nil
}

// attachmentsField names the field of an upload route refusal as the caller
// knows it: the caller never sent "files", only Attachments. A 404 comes from a
// PostHog without the upload route or a project that can't send email from
// code; the client can't tell which, so it keeps PostHog's answer and names
// the attachments. The request's file i is attachment first+i.
func attachmentsField(err *EmailError, first int) string {
	switch {
	case err.Field == "files" || (err.Field == "" && err.StatusCode == http.StatusNotFound):
		return "Attachments"
	case strings.HasPrefix(err.Field, "files__"):
		index, _, _ := strings.Cut(strings.TrimPrefix(err.Field, "files__"), "__")
		if i, convErr := strconv.Atoi(index); convErr == nil {
			return "Attachments[" + strconv.Itoa(first+i) + "]"
		}
		return "Attachments"
	}
	return err.Field
}

// uploadTargets reads PostHog's answer to the upload request. A URL expires
// at its url_expires_at or 15 minutes after the answer, whichever comes
// first, so a client clock behind PostHog's can't keep an expired URL.
func (r emailReply) uploadTargets(files []attachment, answeredAt time.Time) ([]*uploadTarget, *EmailError) {
	slots, _ := r.fields["files"].([]interface{})
	if len(slots) != len(files) {
		return nil, r.apiError("PostHog answered without an upload slot for each file.")
	}
	targets := make([]*uploadTarget, len(files))
	for i, item := range slots {
		fields, _ := item.(map[string]interface{})
		slot := emailReply{fields: fields}
		if slot.text("sha256") != files[i].SHA256 {
			return nil, r.apiError("PostHog answered with upload slots for other files.")
		}
		switch slot.text("status") {
		case "present":
		case "upload":
			target := &uploadTarget{url: slot.text("url"), method: slot.text("method"), headers: map[string]string{}}
			if target.url == "" || target.method == "" {
				return nil, r.apiError("PostHog answered without where to upload " + files[i].Filename + ".")
			}
			target.expiresAt = answeredAt.Add(emailUploadTimeout)
			if expiresAt, err := time.Parse(time.RFC3339Nano, slot.text("url_expires_at")); err == nil && expiresAt.Before(target.expiresAt) {
				target.expiresAt = expiresAt
			}
			headers, _ := fields["headers"].(map[string]interface{})
			for name, value := range headers {
				target.headers[name], _ = value.(string)
			}
			targets[i] = target
		default:
			return nil, r.apiError("PostHog answered with an upload status the client doesn't know.")
		}
	}
	return targets, nil
}

// upload sends a file's bytes to file storage with exactly the headers PostHog
// signed, and never the secret key.
func (s *emailSender) upload(ctx context.Context, file attachment, target uploadTarget) *EmailError {
	ctx, cancel := context.WithTimeout(ctx, emailUploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, target.method, target.url, bytes.NewReader(file.content))
	if err != nil {
		return newEmailError("unexpected_error", "The client could not build the upload of "+file.Filename+".", withoutURL(err))
	}
	for name, value := range target.headers {
		if strings.EqualFold(name, "Host") {
			req.Host = value
			continue
		}
		req.Header.Set(name, value)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return newEmailError("network_error", "Uploading "+file.Filename+" failed. No email was sent.", withoutURL(err))
	}
	defer res.Body.Close()
	retryAfter, _ := parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxBucketAnswerBytes))
	if err != nil {
		cutOff := newEmailError("network_error", "Reading file storage's answer to the upload of "+file.Filename+" failed. No email was sent.", withoutURL(err))
		cutOff.StatusCode = res.StatusCode
		cutOff.RetryAfter = retryAfter
		return cutOff
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	refused := uploadRefused(file.Filename, res.StatusCode, answer)
	refused.RetryAfter = retryAfter
	return refused
}

// uploadRefused is file storage's refusal of an upload, with its error code
// and message when it gave them. A 5xx, a 429 and a RequestTimeout (a stalled
// upload, which S3 answers with a 400) are retried, as S3 asks; any other
// refusal fails the send.
func uploadRefused(filename string, status int, answer []byte) *EmailError {
	var refusal struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	message := "Uploading " + filename + " failed: file storage answered HTTP " + strconv.Itoa(status)
	if xml.Unmarshal(answer, &refusal) == nil && refusal.Code != "" {
		message += ", " + refusal.Code
		if refusal.Message != "" {
			message += ": " + strings.TrimRight(refusal.Message, ".")
		}
	}
	err := newEmailError("attachment_upload_failed", message+". No email was sent.", nil)
	err.StatusCode = status
	switch {
	case status == http.StatusTooManyRequests:
		err.Name = EmailErrorRateLimitExceeded
	case status >= http.StatusInternalServerError || refusal.Code == "RequestTimeout":
		err.Name = EmailErrorServiceUnavailable
	}
	return err
}

// withoutURL drops every URL from a request error. An upload URL is signed,
// and so is a storage redirect, which Go quotes when it can't parse the
// redirect's Location, so neither may reach logs. An error whose text names no
// URL is kept as it is, so a caller can still tell a timeout or a refused
// connection.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if text := err.Error(); !strings.Contains(text, "://") && !strings.Contains(text, "?") {
		return err
	}
	const redacted = "the request failed with an error naming a URL, left out because it may be signed"
	for _, kind := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) {
			return fmt.Errorf(redacted+": %w", kind)
		}
	}
	return errors.New(redacted)
}
