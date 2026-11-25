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

var (
	testScheduledAt = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	testClientNow   = time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC)
)

// newCloudEmailClient is a client on PostHog Cloud US, at testClientNow, whose
// requests reach server, since only a Cloud client schedules.
func newCloudEmailClient(t *testing.T, server *emailServer, options ...emailClientOption) Client {
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	c := newEmailClientFor(t, server.URL, func(config *Config) {
		config.Endpoint, config.AppHost, config.AllowInsecureAppHost = "https://us.i.posthog.com", "", false
		config.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(r)
		})
		for _, option := range options {
			option(config)
		}
	})
	senderOf(c).now = func() time.Time { return testClientNow }
	return c
}

func scheduled(at string) stubReply {
	return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":false,"status":"scheduled","scheduled_at":"` + at + `"}`}
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
			require.Equal(t, "/api/messaging/send/email/", requests[0].Path)
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

func TestEmailsSendRoundsATimeBeforeGosZeroTimeToOnePostHogAccepts(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":false,"status":"queued","scheduled_at":null}`})
	client := newCloudEmailClient(t, server)
	email := templateEmail()
	email.ScheduledAt = time.Time{}.Add(-time.Nanosecond)

	result, err := client.Emails().Send(context.Background(), email)

	require.NoError(t, err)
	require.Equal(t, "queued", result.Status, "PostHog sends a past time now")
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, "0001-01-01T00:00:00.001Z", requests[0].Body["scheduled_at"])
}

func TestEmailsSendKeepsATimeThatRoundsToTheZeroTime(t *testing.T) {
	server := newEmailServer(t)
	client := newEmailClient(t, server)
	email := templateEmail()
	email.ScheduledAt = time.Time{}.Add(-time.Nanosecond)

	_, err := client.Emails().Send(context.Background(), email)

	require.Equal(t, "scheduling_requires_cloud", requireEmailError(t, err).Code)
	require.Empty(t, server.received())
}

func TestEmailsSendScheduledAtOnlyOnPostHogCloud(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		sendURL  string
		inError  string
		schedule bool
	}{
		{name: "US ingestion host", config: Config{Endpoint: "https://us.i.posthog.com"}, sendURL: "https://us.posthog.com/api/messaging/send/email/", schedule: true},
		{name: "EU app host", config: Config{AppHost: "https://eu.posthog.com/"}, sendURL: "https://eu.posthog.com/api/messaging/send/email/", schedule: true},
		{name: "legacy app host", config: Config{Endpoint: "https://app.posthog.com"}, sendURL: "https://us.posthog.com/api/messaging/send/email/", schedule: true},
		{name: "self-hosted", config: Config{AppHost: "https://posthog.example.com"}, inError: "https://posthog.example.com"},
		{name: "reverse proxy in front of Cloud", config: Config{Endpoint: "https://us.i.posthog.com", AppHost: "https://e.example.com"}, inError: "https://e.example.com"},
		{name: "local http host", config: Config{AppHost: "http://localhost:8010", AllowInsecureAppHost: true}, inError: "http://localhost:8010"},
		{name: "proxy with credentials in its URL", config: Config{AppHost: "https://user:hunter2@e.example.com/posthog?token=abc"}, inError: "https://e.example.com"},
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
				require.Equal(t, []string{tt.sendURL}, urls)
				return
			}
			emailErr := requireEmailError(t, err)
			require.Equal(t, "scheduling_requires_cloud", emailErr.Code)
			require.Equal(t, EmailErrorValidation, emailErr.Name)
			require.Equal(t, "ScheduledAt", emailErr.Field)
			require.Equal(t, "This client sends email to "+tt.inError+", and only PostHog Cloud schedules emails, so no email was sent.", emailErr.Message)
			require.Zero(t, emailErr.StatusCode)
			require.Empty(t, urls)
		})
	}
}

func TestEmailsSendChecksTheScheduleInTheAnswer(t *testing.T) {
	reply := func(fields string) stubReply {
		return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":false` + fields + `}`}
	}
	replay := func(fields string) stubReply {
		return stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","replayed":true` + fields + `}`}
	}
	tests := []struct {
		name        string
		at          time.Time
		reply       stubReply
		wantStatus  string
		wantAt      time.Time
		key         string
		unconfirmed bool
	}{
		{name: "the requested time", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.000Z"`), wantStatus: "scheduled", wantAt: testScheduledAt},
		{name: "the requested time in another offset", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T10:00:00+01:00"`), wantStatus: "scheduled", wantAt: testScheduledAt},
		{name: "a past time sent now", at: testClientNow.Add(-time.Hour), reply: reply(`,"status":"queued","scheduled_at":null`), wantStatus: "queued"},
		{name: "a time a minute ahead sent now", at: testClientNow.Add(time.Minute), reply: reply(`,"status":"queued","scheduled_at":null`), wantStatus: "queued"},
		{name: "a time more than a minute ahead sent now", at: testClientNow.Add(time.Minute + time.Millisecond), reply: reply(`,"status":"queued","scheduled_at":null`), unconfirmed: true},
		{name: "a replay of a send now, for a time a minute ahead", at: testClientNow.Add(time.Minute), reply: replay(`,"status":"queued","scheduled_at":null`), wantStatus: "queued", key: "trial-ending:user_123"},
		{name: "a replay of a send now, for a past time", at: testClientNow.Add(-time.Hour), reply: replay(`,"status":"queued","scheduled_at":null`), wantStatus: "queued", key: "trial-ending:user_123"},
		{name: "a replay of a send now, for a time more than a minute ahead", at: testClientNow.Add(time.Minute + time.Millisecond), reply: replay(`,"status":"queued","scheduled_at":null`), unconfirmed: true, key: "trial-ending:user_123"},
		{name: "a replay of the requested time", reply: replay(`,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.000Z"`), wantStatus: "scheduled", wantAt: testScheduledAt, key: "trial-ending:user_123"},
		{name: "another time", reply: reply(`,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.001Z"`), unconfirmed: true},
		{name: "no scheduled_at", reply: reply(``), unconfirmed: true},
		{name: "no scheduled_at with status queued", at: testClientNow.Add(-time.Hour), reply: reply(`,"status":"queued"`), unconfirmed: true},
		{name: "no scheduled_at with status queued for a time ahead", at: testClientNow.Add(time.Minute + time.Millisecond), reply: reply(`,"status":"queued"`), unconfirmed: true},
		{name: "scheduled without a time", reply: reply(`,"status":"scheduled","scheduled_at":null`), unconfirmed: true},
		{name: "queued with a time", at: testClientNow.Add(-time.Hour), reply: reply(`,"status":"queued","scheduled_at":"2026-11-30T08:00:00.000Z"`), unconfirmed: true},
		{name: "a time without a status", reply: reply(`,"scheduled_at":"2026-12-01T09:00:00.000Z"`), unconfirmed: true},
		{name: "a time that doesn't parse", reply: reply(`,"status":"scheduled","scheduled_at":"tomorrow"`), unconfirmed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replies := []stubReply{tt.reply}
			if tt.key != "" {
				first := tt.reply
				first.body = strings.Replace(first.body, `"replayed":true`, `"replayed":false`, 1)
				replies = []stubReply{first, tt.reply}
			}
			server := newEmailServer(t, replies...)
			client := newCloudEmailClient(t, server)
			email := templateEmail()
			email.ScheduledAt = testScheduledAt
			if !tt.at.IsZero() {
				email.ScheduledAt = tt.at
			}
			email.IdempotencyKey = tt.key
			if tt.key != "" {
				_, _ = client.Emails().Send(context.Background(), email)
			}

			result, err := client.Emails().Send(context.Background(), email)

			requests := server.received()
			require.Len(t, requests, len(replies))
			for _, request := range requests {
				require.Equal(t, requests[0].Body, request.Body, "a replay repeats the first send")
				if tt.key != "" {
					require.Equal(t, tt.key, request.Header.Get("Idempotency-Key"))
				}
			}
			if tt.unconfirmed {
				require.Nil(t, result)
				emailErr := requireEmailError(t, err)
				require.Equal(t, "schedule_not_confirmed", emailErr.Code)
				require.Equal(t, EmailErrorApplication, emailErr.Name)
				require.Equal(t, testMessageID, emailErr.MessageID)
				require.Equal(t, http.StatusAccepted, emailErr.StatusCode)
				require.Equal(t, "ScheduledAt", emailErr.Field)
				require.Contains(t, emailErr.Message, testMessageID)
				require.Equal(t, "Read what happened to the email with Emails.Get(err.MessageID) before you send it again: another send can queue a second email.", emailErr.Fix)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testMessageID, result.ID)
			require.Equal(t, tt.wantStatus, result.Status)
			require.True(t, result.ScheduledAt.Equal(tt.wantAt), "got %v", result.ScheduledAt)
		})
	}
}

func TestEmailsSendReplaysAScheduledSend(t *testing.T) {
	replay := `{"message_id":"` + testMessageID + `","replayed":true,"status":"scheduled","scheduled_at":"2026-12-01T09:00:00.000Z"}`
	server := newEmailServer(t, scheduled("2026-12-01T09:00:00.000Z"), stubReply{status: http.StatusAccepted, body: replay})
	client := newCloudEmailClient(t, server)
	email := templateEmail()
	email.IdempotencyKey = "trial-ending:user_123"
	email.ScheduledAt = testScheduledAt

	first, err := client.Emails().Send(context.Background(), email)
	require.NoError(t, err)
	again, err := client.Emails().Send(context.Background(), email)

	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, &SendEmailResult{ID: testMessageID, Replayed: true, Warnings: []EmailWarning{}, Status: "scheduled", ScheduledAt: testScheduledAt}, again)
	requests := server.received()
	require.Len(t, requests, 2)
	for _, request := range requests {
		require.Equal(t, "trial-ending:user_123", request.Header.Get("Idempotency-Key"))
		require.Equal(t, "2026-12-01T09:00:00.000Z", request.Body["scheduled_at"])
	}
	require.Equal(t, requests[0].Body, requests[1].Body)
}

const sandboxSenderWarning = `"warnings":[{"code":"sandbox_sender","message":"Sent from the PostHog test sender."}]`

func TestEmailsSendLogsWarningsOfAnUnconfirmedSchedule(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `",` + sandboxSenderWarning + `}`})
	logs := &recordingLogger{}
	client := newCloudEmailClient(t, server, func(config *Config) { config.Logger = logs })
	email := templateEmail()
	email.ScheduledAt = testScheduledAt

	_, err := client.Emails().Send(context.Background(), email)

	require.Equal(t, "schedule_not_confirmed", requireEmailError(t, err).Code)
	require.Equal(t, []string{"PostHog email warning sandbox_sender: Sent from the PostHog test sender."}, logs.warnings())
}

// slowLogger moves the client's clock on while it logs, as a slow log sink
// would.
type slowLogger struct {
	recordingLogger
	clock *time.Time
}

func (l *slowLogger) Warnf(format string, args ...interface{}) {
	*l.clock = l.clock.Add(2 * time.Second)
	l.recordingLogger.Warnf(format, args...)
}

func TestEmailsSendChecksTheScheduleAgainstTheClockWhenTheAnswerArrives(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusAccepted, body: `{"message_id":"` + testMessageID + `","status":"queued","scheduled_at":null,` + sandboxSenderWarning + `}`})
	clock := testClientNow
	client := newCloudEmailClient(t, server, func(config *Config) { config.Logger = &slowLogger{clock: &clock} })
	senderOf(client).now = func() time.Time { return clock }
	email := templateEmail()
	email.ScheduledAt = testClientNow.Add(maxEmailClockSkew + time.Second)

	_, err := client.Emails().Send(context.Background(), email)

	require.Equal(t, "schedule_not_confirmed", requireEmailError(t, err).Code)
}

func TestEmailsSendChecksAReplayAgainstTheAttemptThatMayHaveQueued(t *testing.T) {
	queuedNow := func(replayed string) func() *http.Response {
		return func() *http.Response {
			body := `{"message_id":"` + testMessageID + `","replayed":` + replayed + `,"status":"queued","scheduled_at":null}`
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
		}
	}
	type attempt struct {
		reply func() *http.Response
		takes time.Duration
	}
	tests := []struct {
		name     string
		after    time.Duration
		attempts []attempt
		wantCode string
	}{
		{
			// The cut-off first answer may have sent the email at once, 90 seconds
			// before ScheduledAt minus the allowed skew.
			name:  "a lost answer, then a replay close to the time",
			after: 3 * time.Minute,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusAccepted, Body: cutOffBody{strings.NewReader(`{"message_id":"`)}, Header: http.Header{}}
				}, takes: 30 * time.Second},
				{reply: queuedNow("true"), takes: 2 * time.Minute},
			},
			wantCode: "schedule_not_confirmed",
		},
		{
			// PostHog chose to send at once when the attempt began, 70 seconds
			// early, not when its answer timed out.
			name:  "a slow lost answer, then a replay",
			after: 70 * time.Second,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusAccepted, Body: cutOffBody{strings.NewReader(`{"message_id":"`)}, Header: http.Header{}}
				}, takes: 15 * time.Second},
				{reply: queuedNow("true"), takes: time.Second},
			},
			wantCode: "schedule_not_confirmed",
		},
		{
			// An earlier request still holds the key, so it may have queued the
			// email before this send's retry.
			name:  "keys in flight, then a replay close to the time",
			after: 80 * time.Second,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(`{"code":"idempotency_key_in_flight","detail":"Try again."}`)), Header: http.Header{}}
				}, takes: 30 * time.Second},
				{reply: queuedNow("true"), takes: time.Second},
			},
			wantCode: "schedule_not_confirmed",
		},
		{
			// A rate limit queued nothing, so the send that queued is the second.
			name:  "a rate limit, then a send close to the time",
			after: 80 * time.Second,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{"code":"rate_limited","detail":"Slow down."}`)), Header: http.Header{}}
				}, takes: 30 * time.Second},
				{reply: queuedNow("false"), takes: 90 * time.Second},
			},
		},
		{
			// PostHog answers a retry as a first send when its key lookup runs late,
			// so replayed false doesn't prove the 503 queued nothing.
			name:  "a 503, then a first answer close to the time",
			after: 100 * time.Second,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"code":"delivery_unavailable","detail":"Try again."}`)), Header: http.Header{}}
				}, takes: 30 * time.Second},
				{reply: queuedNow("false"), takes: 2 * time.Minute},
			},
			wantCode: "schedule_not_confirmed",
		},
		{
			// A cut-off 429 still says PostHog refused the attempt, so the send that
			// may have queued is the second, after the time.
			name:  "a cut-off rate limit, then a lost answer after the time",
			after: 80 * time.Second,
			attempts: []attempt{
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusTooManyRequests, Body: cutOffBody{strings.NewReader(`{"code":"rate`)}, Header: http.Header{}}
				}, takes: 30 * time.Second},
				{reply: func() *http.Response {
					return &http.Response{StatusCode: http.StatusAccepted, Body: cutOffBody{strings.NewReader(`{"message_id":"`)}, Header: http.Header{}}
				}, takes: 2 * time.Minute},
				{reply: queuedNow("true"), takes: 10 * time.Second},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := testClientNow
			n := 0
			var key string
			client := newCloudEmailClient(t, newEmailServer(t), func(config *Config) {
				config.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					// PostHog replays only a retry with the first attempt's key.
					if n == 0 {
						key = r.Header.Get("Idempotency-Key")
					}
					require.NotEmpty(t, key)
					require.Equal(t, key, r.Header.Get("Idempotency-Key"))
					next := tt.attempts[n]
					n++
					clock = clock.Add(next.takes)
					return next.reply(), nil
				})
			})
			senderOf(client).now = func() time.Time { return clock }
			email := templateEmail()
			email.ScheduledAt = testClientNow.Add(tt.after)

			_, err := client.Emails().Send(context.Background(), email)

			require.Equal(t, len(tt.attempts), n)
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, tt.wantCode, requireEmailError(t, err).Code)
		})
	}
}

func TestEmailsSendSurfacesARefusedScheduleWithoutSendingNow(t *testing.T) {
	server := newEmailServer(t, stubReply{status: http.StatusBadRequest, body: `{"type":"validation_error","code":"scheduled_too_far","detail":"` + "`scheduled_at`" + ` is more than 7 days away, so PostHog didn't send the email.","attr":"scheduled_at","why":"PostHog holds a scheduled email for at most 7 days after the request.","fix":"Pick a time within 7 days, or send the email closer to the time."}`})
	client := newCloudEmailClient(t, server)
	email := codeEmail()
	email.ScheduledAt = testClientNow.Add(8 * 24 * time.Hour)

	result, err := client.Emails().Send(context.Background(), email)

	require.Nil(t, result)
	emailErr := requireEmailError(t, err)
	require.Equal(t, "scheduled_too_far", emailErr.Code)
	require.Equal(t, EmailErrorValidation, emailErr.Name)
	require.Equal(t, "scheduled_at", emailErr.Field)
	require.Equal(t, http.StatusBadRequest, emailErr.StatusCode)
	require.Empty(t, emailErr.MessageID)
	requests := server.received()
	require.Len(t, requests, 1)
	require.Equal(t, "2026-12-08T09:00:00.000Z", requests[0].Body["scheduled_at"])
}
