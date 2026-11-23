package posthog

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testScheduledAt = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

// newCloudEmailClient is a client on PostHog Cloud US whose requests reach
// server, since only a Cloud client schedules.
func newCloudEmailClient(t *testing.T, server *emailServer) Client {
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	c, err := NewWithConfig("phc_test", Config{Endpoint: "https://us.i.posthog.com", SecretKey: "phs_test", Transport: transport, Logger: testLogger{t.Logf, t.Logf}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	senderOf(c).backoff = func(int) time.Duration { return 0 }
	return c
}

func scheduled(at string) stubReply {
	return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":false,"warnings":[],"status":"scheduled","scheduled_at":"` + at + `"}`}
}

func codeEmail() CodeEmail {
	return CodeEmail{To: "user@example.com", TemplateKey: "welcome", Subject: "Hi", HTML: "<p>Hi</p>"}
}

func TestEmailsSendScheduledAtOnBothForms(t *testing.T) {
	berlin := time.FixedZone("UTC+2", 2*60*60)
	at := time.Date(2026, 12, 1, 11, 0, 0, 0, berlin)
	template := templateEmail()
	template.ScheduledAt = at
	code := codeEmail()
	code.ScheduledAt = at
	for name, email := range map[string]Email{"template": template, "code": code} {
		t.Run(name, func(t *testing.T) {
			server := newEmailServer(t, scheduled("2026-12-01T09:00:00.000Z"))
			client := newCloudEmailClient(t, server)

			result, err := client.Emails().Send(context.Background(), email)

			require.NoError(t, err)
			require.Equal(t, "scheduled", result.Status)
			require.True(t, result.ScheduledAt.Equal(testScheduledAt), "got %v", result.ScheduledAt)
			require.Equal(t, time.UTC, result.ScheduledAt.Location())
			requests := server.received()
			require.Len(t, requests, 1)
			require.Equal(t, "2026-12-01T09:00:00.000Z", requests[0].Body["scheduled_at"])
		})
	}
}

func TestEmailsSendRoundsScheduledAtUpToTheMillisecond(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "a nanosecond past a millisecond", at: testScheduledAt.Add(123*time.Millisecond + time.Nanosecond), want: "2026-12-01T09:00:00.124Z"},
		{name: "a microsecond short of a second", at: testScheduledAt.Add(time.Second - time.Microsecond), want: "2026-12-01T09:00:01.000Z"},
		{name: "a whole millisecond", at: testScheduledAt.Add(123 * time.Millisecond), want: "2026-12-01T09:00:00.123Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, failed(http.StatusServiceUnavailable, "service_unavailable"), scheduled(tt.want))
			client := newCloudEmailClient(t, server)
			email := templateEmail()
			email.ScheduledAt = tt.at

			result, err := client.Emails().Send(context.Background(), email)

			require.NoError(t, err)
			require.Equal(t, tt.want, result.ScheduledAt.Format("2006-01-02T15:04:05.000Z07:00"))
			requests := server.received()
			require.Len(t, requests, 2)
			for _, request := range requests {
				require.Equal(t, tt.want, request.Body["scheduled_at"])
			}
		})
	}
}

func TestEmailsSendScheduledAtOnlyOnPostHogCloud(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		appHost  string
		schedule bool
	}{
		{name: "US ingestion host", config: Config{Endpoint: "https://us.i.posthog.com"}, appHost: "https://us.posthog.com", schedule: true},
		{name: "EU app host", config: Config{AppHost: "https://eu.posthog.com/"}, appHost: "https://eu.posthog.com", schedule: true},
		{name: "legacy app host", config: Config{Endpoint: "https://app.posthog.com"}, appHost: "https://us.posthog.com", schedule: true},
		{name: "self-hosted", config: Config{AppHost: "https://posthog.example.com"}, appHost: "https://posthog.example.com"},
		{name: "reverse proxy in front of Cloud", config: Config{Endpoint: "https://us.i.posthog.com", AppHost: "https://e.example.com"}, appHost: "https://e.example.com"},
		{name: "local http host", config: Config{AppHost: "http://localhost:8010", AllowInsecureAppHost: true}, appHost: "http://localhost:8010"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var urls []string
			tt.config.SecretKey = "phs_test"
			tt.config.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				urls = append(urls, r.URL.String())
				return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"message_id":"msg_1","status":"scheduled","scheduled_at":"2026-12-01T09:00:00.000Z"}`)), Header: http.Header{}}, nil
			})
			client, err := NewWithConfig("phc_test", tt.config)
			require.NoError(t, err)
			defer client.Close()
			email := codeEmail()
			email.ScheduledAt = testScheduledAt
			if !tt.schedule {
				email.Attachments = []EmailAttachment{{Filename: "invoice.pdf", Content: []byte("%PDF")}}
			}

			result, err := client.Emails().Send(context.Background(), email)

			if tt.schedule {
				require.NoError(t, err)
				require.Equal(t, "scheduled", result.Status)
				require.Equal(t, []string{tt.appHost + emailSendPath}, urls)
				return
			}
			emailErr := requireEmailError(t, err)
			require.Equal(t, "scheduling_unsupported", emailErr.Code)
			require.Equal(t, EmailErrorValidation, emailErr.Name)
			require.Equal(t, "ScheduledAt", emailErr.Field)
			require.Equal(t, "This client sends email to "+tt.appHost+", and only PostHog Cloud schedules emails, so no email was sent.", emailErr.Message)
			require.Zero(t, emailErr.StatusCode)
			require.Empty(t, urls)
		})
	}
}

func TestEmailsSendChecksTheScheduleInTheAnswer(t *testing.T) {
	reply := func(fields string) stubReply {
		return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":true,"warnings":[]` + fields + `}`}
	}
	tests := []struct {
		name        string
		reply       stubReply
		wantStatus  string
		wantAt      time.Time
		unconfirmed bool
	}{
		{name: "the requested time", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.000Z"`), wantStatus: "scheduled", wantAt: testScheduledAt},
		{name: "the requested time in another offset", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T10:00:00+01:00"`), wantStatus: "scheduled", wantAt: testScheduledAt},
		{name: "a past time sent now", reply: reply(`,"status":"queued","scheduled_at":null`), wantStatus: "queued"},
		{name: "another time", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.001Z"`), unconfirmed: true},
		{name: "no scheduled_at", reply: reply(``), unconfirmed: true},
		{name: "no scheduled_at with status queued", reply: reply(`,"status":"queued"`), unconfirmed: true},
		{name: "scheduled without a time", reply: reply(`,"status":"scheduled","scheduled_at":null`), unconfirmed: true},
		{name: "queued with a time", reply: reply(`,"status":"queued","scheduled_at":"2026-12-01T09:00:00.000Z"`), unconfirmed: true},
		{name: "a time without a status", reply: reply(`,"scheduled_at":"2026-12-01T09:00:00.000Z"`), unconfirmed: true},
		{name: "a time that doesn't parse", reply: reply(`,"status":"scheduled","scheduled_at":"tomorrow"`), unconfirmed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, tt.reply)
			client := newCloudEmailClient(t, server)
			email := templateEmail()
			email.ScheduledAt = testScheduledAt

			result, err := client.Emails().Send(context.Background(), email)

			require.Len(t, server.received(), 1)
			if tt.unconfirmed {
				require.Nil(t, result)
				emailErr := requireEmailError(t, err)
				require.Equal(t, "scheduling_unconfirmed", emailErr.Code)
				require.Equal(t, EmailErrorApplication, emailErr.Name)
				require.Equal(t, testMessageID, emailErr.MessageID)
				require.Equal(t, http.StatusAccepted, emailErr.StatusCode)
				require.Equal(t, "ScheduledAt", emailErr.Field)
				require.Contains(t, emailErr.Message, testMessageID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testMessageID, result.ID)
			require.True(t, result.Replayed)
			require.Equal(t, tt.wantStatus, result.Status)
			require.True(t, result.ScheduledAt.Equal(tt.wantAt), "got %v", result.ScheduledAt)
		})
	}
}

func TestEmailsSendUnconfirmedScheduleSaysWhetherToSendAgain(t *testing.T) {
	tests := []struct {
		name           string
		idempotencyKey string
		wantFix        string
	}{
		{name: "caller's key", idempotencyKey: "welcome:user_123", wantFix: "Read what happened to the email with Emails.Get(err.MessageID). Sending it again with the same IdempotencyKey returns the same answer."},
		{name: "client's key", wantFix: "Read what happened to the email with Emails.Get(err.MessageID). Don't send it again: the client chose this send's IdempotencyKey, so a new send with any key queues a second email."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEmailServer(t, accepted(testMessageID))
			client := newCloudEmailClient(t, server)
			email := templateEmail()
			email.ScheduledAt = testScheduledAt
			email.IdempotencyKey = tt.idempotencyKey

			_, err := client.Emails().Send(context.Background(), email)

			require.Equal(t, tt.wantFix, requireEmailError(t, err).Fix)
		})
	}
}

func TestEmailsSendSurfacesARefusedScheduleWithoutSendingNow(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusBadRequest, body: `{"type":"validation_error","code":"invalid_input","detail":"scheduled_at is more than 7 days ahead.","attr":"scheduled_at"}`})
	client := newCloudEmailClient(t, server)
	email := codeEmail()
	email.ScheduledAt = testScheduledAt

	result, err := client.Emails().Send(context.Background(), email)

	require.Nil(t, result)
	emailErr := requireEmailError(t, err)
	require.Equal(t, EmailErrorValidation, emailErr.Name)
	require.Equal(t, "scheduled_at", emailErr.Field)
	require.Equal(t, http.StatusBadRequest, emailErr.StatusCode)
	require.Empty(t, emailErr.MessageID)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, "2026-12-01T09:00:00.000Z", requests[0].Body["scheduled_at"])
}

func TestEmailsGetReadsAScheduledEmail(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusOK, body: `{"id":"` + testMessageID + `","status":"scheduled","created_at":"2026-11-30T09:00:00Z","status_at":"2026-11-30T09:00:00Z","reason":null}`})
	client := newEmailClient(t, server)

	status, err := client.Emails().Get(context.Background(), testMessageID)

	require.NoError(t, err)
	require.Equal(t, "scheduled", status.Status)
}
