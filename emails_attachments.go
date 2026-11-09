package posthog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
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
	// headers and MIME boundaries when it estimates its size.
	emailHeadersAndMIMEBytes = 64 * 1024
	// emailUploadTimeout is how long an upload URL accepts the upload.
	emailUploadTimeout   = 15 * time.Minute
	maxBucketAnswerBytes = 64 << 10
)

// attachment is an EmailAttachment named by the SHA-256 of its content.
type attachment struct {
	EmailAttachment
	sha256 string
}

type attachmentRef struct {
	SHA256      string `json:"sha256"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
}

type uploadRequestFile struct {
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// uploadTarget is where PostHog asked the client to upload one file.
type uploadTarget struct {
	url     string
	method  string
	headers map[string]string
}

func checkAttachments(attachments []EmailAttachment, bodies ...string) ([]attachment, *EmailError) {
	if len(attachments) > maxEmailAttachments {
		return nil, invalidEmail("too_many_attachments", "Attachments", "An email carries at most 10 attachments.")
	}
	files := make([]attachment, 0, len(attachments))
	sizes := make([]int, 0, len(attachments)+len(bodies))
	for i, file := range attachments {
		field := "Attachments[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(file.Filename) == "" {
			return nil, invalidEmail("invalid_attachment", field+".Filename", "An attachment needs a Filename, such as \"invoice.pdf\".")
		}
		if len(file.Content) == 0 {
			return nil, invalidEmail("invalid_attachment", field+".Content", "An attachment needs Content, the file's bytes.")
		}
		sum := sha256.Sum256(file.Content)
		files = append(files, attachment{EmailAttachment: file, sha256: hex.EncodeToString(sum[:])})
		sizes = append(sizes, len(file.Content))
	}
	for _, body := range bodies {
		sizes = append(sizes, len(body))
	}
	if len(files) > 0 && estimateMessageBytes(sizes) > maxEmailMessageBytes {
		return nil, invalidEmail("message_too_large", "Attachments", "The email would be over 40 MB after Base64 encoding, PostHog's limit. Send fewer or smaller files, about 29 MB in total, or link to large files from the body.")
	}
	return files, nil
}

// estimateMessageBytes is PostHog's estimate of a message whose files and
// bodies have these sizes: each part Base64 encoded in 76-character lines,
// plus room for headers and MIME boundaries.
func estimateMessageBytes(partSizes []int) int64 {
	total := int64(emailHeadersAndMIMEBytes)
	for _, size := range partSizes {
		encoded := (int64(size) + 2) / 3 * 4
		total += (encoded*78 + 75) / 76
	}
	return total
}

func references(files []attachment) []attachmentRef {
	refs := make([]attachmentRef, 0, len(files))
	for _, file := range files {
		refs = append(refs, attachmentRef{SHA256: file.sha256, Filename: file.Filename, ContentType: file.ContentType, ContentID: file.ContentID})
	}
	return refs
}

// uploadAttachments asks PostHog which files it lacks and uploads each one,
// so a send can name them by their hashes. It returns at the first failure.
func (s *emailSender) uploadAttachments(ctx context.Context, maxRetries int, files []attachment) *EmailError {
	requested := make([]uploadRequestFile, 0, len(files))
	for _, file := range files {
		requested = append(requested, uploadRequestFile{SHA256: file.sha256, Size: len(file.Content)})
	}
	body, err := encodeEmailBody(map[string]interface{}{"files": requested})
	if err != nil {
		return newEmailError("unexpected_error", "The client could not encode the upload request.", err)
	}
	header := s.header()
	header.Set("Content-Type", "application/json")
	reply, emailErr := s.request(ctx, maxRetries, http.MethodPost, s.appHost+emailUploadsPath, header, body)
	if emailErr != nil {
		return emailErr
	}
	targets, emailErr := reply.uploadTargets(files)
	if emailErr != nil {
		return emailErr
	}
	for i, target := range targets {
		if target == nil {
			continue
		}
		if emailErr := s.retry(ctx, maxRetries, func(ctx context.Context) *EmailError {
			return s.upload(ctx, files[i], *target)
		}); emailErr != nil {
			emailErr.Field = "Attachments[" + strconv.Itoa(i) + "]"
			return emailErr
		}
	}
	return nil
}

// uploadTargets reads PostHog's answer to the upload request: per file, in
// request order, nil when PostHog has the file and a target otherwise.
func (r emailReply) uploadTargets(files []attachment) ([]*uploadTarget, *EmailError) {
	slots, _ := r.fields["files"].([]interface{})
	if len(slots) != len(files) {
		return nil, r.apiError("PostHog answered without an upload slot for each file.")
	}
	targets := make([]*uploadTarget, len(files))
	for i, item := range slots {
		fields, _ := item.(map[string]interface{})
		slot := emailReply{fields: fields}
		if slot.text("sha256") != files[i].sha256 {
			return nil, r.apiError("PostHog answered with upload slots for other files.")
		}
		switch slot.text("status") {
		case "present":
		case "upload":
			target := &uploadTarget{url: slot.text("url"), method: slot.text("method"), headers: map[string]string{}}
			if target.url == "" {
				return nil, r.apiError("PostHog answered without the URL to upload " + files[i].Filename + " to.")
			}
			if target.method == "" {
				target.method = http.MethodPut
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

	req, err := http.NewRequestWithContext(ctx, target.method, target.url, bytes.NewReader(file.Content))
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
		return newEmailError("network_error", "Uploading "+file.Filename+" failed.", withoutURL(err))
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxBucketAnswerBytes))
	if err != nil {
		return cutOffAnswer(withoutURL(err), res.StatusCode, 0)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return uploadRefused(file.Filename, res.StatusCode, answer)
}

// uploadRefused is file storage's refusal of an upload, with its error code
// and message when it gave them. A 503 asks the client to slow down, so it is
// retried; any other refusal fails the send.
func uploadRefused(filename string, status int, answer []byte) *EmailError {
	var refusal struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	message := "Uploading " + filename + " failed: file storage answered HTTP " + strconv.Itoa(status)
	if xml.Unmarshal(answer, &refusal) == nil && refusal.Code != "" {
		message += ", " + refusal.Code
		if refusal.Message != "" {
			message += ": " + refusal.Message
		}
	}
	err := newEmailError("attachment_upload_failed", message+". No email was sent.", nil)
	err.StatusCode = status
	if status == http.StatusServiceUnavailable {
		err.Name = EmailErrorServiceUnavailable
	}
	return err
}

// withoutURL drops the URL from a request error. An upload URL is signed, so
// it must not reach logs.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
