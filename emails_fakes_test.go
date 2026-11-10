package posthog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// fakeStorage behaves like S3 behind presigned PUT URLs. It stores an object
// only when the PUT carries what PostHog signed, arrives before the URL
// expires, carries no other credentials, and its bytes match the signed
// SHA-256. Otherwise it answers as S3 does.
type fakeStorage struct {
	*httptest.Server
	clock func() time.Time

	mu      sync.Mutex
	objects map[string][]byte
	// faults are answers to the next PUTs, for failures real storage can't
	// be made to produce on demand. Each is used once, in order.
	faults []storageFault
	// afterStore, when set, runs each time storage has stored an upload.
	afterStore func()
}

type storageFault struct {
	stubReply
	// breakOff ends the answer after its status line and part of its body.
	breakOff bool
}

var fakeStorageSecret = []byte("fake-storage-signing-key")

func newFakeStorage(t *testing.T, clock func() time.Time, faults ...storageFault) *fakeStorage {
	s := &fakeStorage{clock: clock, objects: map[string][]byte{}, faults: faults}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *fakeStorage) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	var fault *storageFault
	if len(s.faults) > 0 {
		fault, s.faults = &s.faults[0], s.faults[1:]
	}
	s.mu.Unlock()
	if fault != nil {
		for name, value := range fault.header {
			w.Header().Set(name, value)
		}
		if fault.breakOff {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(fault.status)
			_, _ = w.Write([]byte("<Error>"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(fault.status)
		_, _ = w.Write([]byte(fault.body))
		return
	}

	key := strings.TrimPrefix(r.URL.Path, "/")
	expiresAt, _ := strconv.ParseInt(r.URL.Query().Get("X-Amz-Expires-At"), 10, 64)
	checksum := r.Header.Get("X-Amz-Checksum-Sha256")
	switch {
	case r.Method != http.MethodPut:
		s.refuse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource.")
	case r.Header.Get("Authorization") != "":
		s.refuse(w, http.StatusBadRequest, "InvalidArgument", "Only one auth mechanism allowed.")
	case r.ContentLength < 0:
		s.refuse(w, http.StatusLengthRequired, "MissingContentLength", "You must provide the Content-Length HTTP header.")
	case !hmac.Equal([]byte(r.URL.Query().Get("X-Amz-Signature")), []byte(s.sign(key, expiresAt, r.ContentLength, checksum))):
		s.refuse(w, http.StatusForbidden, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.")
	case !s.clock().Before(time.Unix(expiresAt, 0)):
		s.refuse(w, http.StatusForbidden, "AccessDenied", "Request has expired")
	case checksum != checksumOf(body):
		s.refuse(w, http.StatusBadRequest, "BadDigest", "The SHA256 you specified did not match the calculated checksum.")
	default:
		s.mu.Lock()
		s.objects[key] = body
		afterStore := s.afterStore
		s.mu.Unlock()
		if afterStore != nil {
			afterStore()
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (s *fakeStorage) refuse(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>%s</Code><Message>%s</Message></Error>`, code, message)
}

func (s *fakeStorage) sign(key string, expiresAt, size int64, checksum string) string {
	mac := hmac.New(sha256.New, fakeStorageSecret)
	_, _ = fmt.Fprintf(mac, "PUT\n%s\n%d\n%d\n%s", key, expiresAt, size, checksum)
	return hex.EncodeToString(mac.Sum(nil))
}

// slot signs an upload of the file with this SHA-256 and size, as PostHog's
// upload route does, for a URL that expires at expiresAt.
func (s *fakeStorage) slot(sha string, size int, expiresAt time.Time) uploadSlot {
	key := objectKey(sha)
	raw, _ := hex.DecodeString(sha)
	checksum := base64.StdEncoding.EncodeToString(raw)
	signature := s.sign(key, expiresAt.Unix(), int64(size), checksum)
	return uploadSlot{
		"sha256": sha,
		"status": "upload",
		"url":    fmt.Sprintf("%s/%s?X-Amz-Expires-At=%d&X-Amz-Signature=%s", s.URL, key, expiresAt.Unix(), signature),
		"method": http.MethodPut,
		"headers": map[string]string{
			"Content-Length":        strconv.Itoa(size),
			"x-amz-checksum-sha256": checksum,
		},
		"url_expires_at": expiresAt.UTC().Format(time.RFC3339),
	}
}

// stored is what storage holds for the file with this SHA-256, or nil.
func (s *fakeStorage) stored(sha string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[objectKey(sha)]
}

func (s *fakeStorage) put(content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[objectKey(sha256Hex(content))] = content
}

func (s *fakeStorage) isEmpty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects) == 0
}

func objectKey(sha string) string { return "attachments/team/2/" + sha }

func checksumOf(content []byte) string {
	sum := sha256.Sum256(content)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type uploadSlot map[string]interface{}

// codeSendFields are the fields PostHog's code send takes.
var codeSendFields = []string{"to", "template_key", "subject", "html", "text", "from", "reply_to", "cc", "bcc", "attachments", "capture_engagement", "distinct_id"}

// fieldsBeforeAttachments are the fields a PostHog from before cc, bcc and
// attachments takes.
var fieldsBeforeAttachments = []string{"to", "template_key", "subject", "html", "text", "from", "reply_to", "capture_engagement", "distinct_id"}

var attachmentFields = map[string]bool{"sha256": true, "filename": true, "content_type": true, "content_id": true}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// fakePostHog behaves like PostHog's upload and code send routes. The upload
// route answers present for a file storage holds and signs an upload for any
// other. The send refuses a field it doesn't take and a file storage doesn't
// hold, and delivers the email otherwise.
type fakePostHog struct {
	*httptest.Server
	storage *fakeStorage
	clock   func() time.Time

	mu sync.Mutex
	// takes are the send fields this PostHog takes.
	takes map[string]bool
	// withoutUploadRoute makes it a PostHog from before the upload route.
	withoutUploadRoute bool
	// uploadFaults and sendFaults answer the upload or send request with
	// this ordinal, from 0, in place of PostHog's own answer.
	uploadFaults map[int]stubReply
	sendFaults   map[int]stubReply
	// urlLifetimes is how long the upload URLs of each upload answer live,
	// in order; 15 minutes past the list.
	urlLifetimes []time.Duration
	// beforeUploadAnswer runs before the upload route answers request n.
	beforeUploadAnswer func(n int)
	uploads, sends     int
	delivered          []deliveredEmail
}

type deliveredEmail struct {
	To, Subject string
	Cc, Bcc     []string
	Attachments []deliveredAttachment
}

type deliveredAttachment struct {
	Filename, ContentType, ContentID string
	Content                          []byte
}

func newFakePostHog(t *testing.T, storage *fakeStorage) *fakePostHog {
	p := &fakePostHog{storage: storage, clock: storage.clock, takes: map[string]bool{}}
	for _, field := range codeSendFields {
		p.takes[field] = true
	}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

// takeOnly makes this a PostHog that takes only these send fields.
func (p *fakePostHog) takeOnly(fields []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.takes = map[string]bool{}
	for _, field := range fields {
		p.takes[field] = true
	}
}

func (p *fakePostHog) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		answer(w, http.StatusBadRequest, map[string]interface{}{"type": "validation_error", "code": "parse_error", "detail": "JSON parse error."})
		return
	}
	switch {
	case r.Method != http.MethodPost:
		answer(w, http.StatusMethodNotAllowed, map[string]interface{}{"type": "invalid_request", "code": "method_not_allowed"})
	case r.URL.Path == emailUploadsPath && !p.withoutUploadRoute:
		p.authorized(w, r, func() { p.answerUploads(w, body) })
	case r.URL.Path == emailSendPath:
		p.authorized(w, r, func() { p.send(w, body) })
	default:
		answer(w, http.StatusNotFound, map[string]interface{}{"type": "invalid_request", "code": "not_found", "detail": "Endpoint not found.", "attr": nil})
	}
}

func (p *fakePostHog) authorized(w http.ResponseWriter, r *http.Request, next func()) {
	if r.Header.Get("Authorization") != "Bearer phs_test" {
		answer(w, http.StatusUnauthorized, map[string]interface{}{"type": "authentication_error", "code": "authentication_failed", "detail": "Use a project secret key."})
		return
	}
	next()
}

func (p *fakePostHog) answerUploads(w http.ResponseWriter, body map[string]interface{}) {
	p.mu.Lock()
	n := p.uploads
	p.uploads++
	before := p.beforeUploadAnswer
	fault, faulted := p.uploadFaults[n]
	lifetime := 15 * time.Minute
	if n < len(p.urlLifetimes) {
		lifetime = p.urlLifetimes[n]
	}
	p.mu.Unlock()
	if before != nil {
		before(n)
	}
	if faulted {
		reply(w, fault)
		return
	}
	files, attr := uploadRequestFiles(body)
	if attr != "" {
		answer(w, http.StatusBadRequest, map[string]interface{}{"type": "validation_error", "code": "invalid_input", "detail": "Invalid upload request.", "attr": attr})
		return
	}
	slots := make([]uploadSlot, 0, len(files))
	for _, file := range files {
		if p.storage.stored(file.SHA256) != nil {
			slots = append(slots, uploadSlot{"sha256": file.SHA256, "status": "present", "expires_at": p.clock().Add(37 * 24 * time.Hour).UTC().Format(time.RFC3339)})
			continue
		}
		slots = append(slots, p.storage.slot(file.SHA256, file.Size, p.clock().Add(lifetime)))
	}
	answer(w, http.StatusOK, map[string]interface{}{"files": slots})
}

// uploadRequestFiles reads an upload request as PostHog does, refusing any
// key it doesn't take, and names the first field it refuses.
func uploadRequestFiles(body map[string]interface{}) ([]uploadRequestFile, string) {
	for key := range body {
		if key != "files" {
			return nil, key
		}
	}
	items, _ := body["files"].([]interface{})
	if len(items) < 1 || len(items) > 10 {
		return nil, "files"
	}
	files := make([]uploadRequestFile, 0, len(items))
	for i, item := range items {
		fields, _ := item.(map[string]interface{})
		sha, _ := fields["sha256"].(string)
		size, _ := fields["size"].(float64)
		if len(fields) != 2 || !sha256Pattern.MatchString(sha) || size < 1 || size != float64(int(size)) {
			return nil, fmt.Sprintf("files__%d", i)
		}
		files = append(files, uploadRequestFile{SHA256: sha, Size: int(size)})
	}
	return files, ""
}

func (p *fakePostHog) send(w http.ResponseWriter, body map[string]interface{}) {
	p.mu.Lock()
	n := p.sends
	p.sends++
	fault, faulted := p.sendFaults[n]
	takes := p.takes
	p.mu.Unlock()
	if faulted {
		reply(w, fault)
		return
	}
	for key := range body {
		if !takes[key] {
			answer(w, http.StatusBadRequest, map[string]interface{}{"type": "validation_error", "code": "invalid_input", "detail": "PostHog doesn't take this field, so it doesn't send the email without it.", "attr": key})
			return
		}
	}
	email := deliveredEmail{To: text(body, "to"), Subject: text(body, "subject"), Cc: texts(body, "cc"), Bcc: texts(body, "bcc")}
	items, _ := body["attachments"].([]interface{})
	for i, item := range items {
		fields, _ := item.(map[string]interface{})
		for key := range fields {
			if !attachmentFields[key] {
				answer(w, http.StatusBadRequest, map[string]interface{}{"type": "invalid_request", "code": "invalid_request", "detail": "An attachment takes `sha256`, `filename`, `content_type` and `content_id`.", "attr": fmt.Sprintf("attachments__%d__%s", i, key)})
				return
			}
		}
		content := p.storage.stored(text(fields, "sha256"))
		if content == nil {
			answer(w, http.StatusBadRequest, map[string]interface{}{"type": "invalid_request", "code": "attachment_not_uploaded", "detail": "PostHog doesn't have this file.", "attr": fmt.Sprintf("attachments__%d__sha256", i)})
			return
		}
		email.Attachments = append(email.Attachments, deliveredAttachment{
			Filename: text(fields, "filename"), ContentType: text(fields, "content_type"), ContentID: text(fields, "content_id"), Content: content,
		})
	}
	p.mu.Lock()
	p.delivered = append(p.delivered, email)
	p.mu.Unlock()
	answer(w, http.StatusAccepted, map[string]interface{}{"message_id": fmt.Sprintf("msg_%d", n+1), "replayed": false, "warnings": []string{}})
}

// deliveredEmails are the emails PostHog accepted to send, in order.
func (p *fakePostHog) deliveredEmails() []deliveredEmail {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]deliveredEmail(nil), p.delivered...)
}

func answer(w http.ResponseWriter, status int, body interface{}) {
	raw, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func reply(w http.ResponseWriter, r stubReply) {
	for name, value := range r.header {
		w.Header().Set(name, value)
	}
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

func text(fields map[string]interface{}, key string) string {
	value, _ := fields[key].(string)
	return value
}

func texts(fields map[string]interface{}, key string) []string {
	items, _ := fields[key].([]interface{})
	var values []string
	for _, item := range items {
		value, _ := item.(string)
		values = append(values, value)
	}
	return values
}
