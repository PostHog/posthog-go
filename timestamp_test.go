package posthog

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func TestEventTimestampSerializationUsesUTC(t *testing.T) {
	timestamp := time.Date(2025, time.January, 2, 3, 4, 5, 600000000, time.FixedZone("UTC-7", -7*60*60))
	const want = "2025-01-02T10:04:05.600Z"

	messages := []struct {
		name string
		msg  Message
	}{
		{name: "capture", msg: Capture{Uuid: "u", Event: "event", DistinctId: "user", Timestamp: timestamp}},
		{name: "identify", msg: Identify{Uuid: "u", DistinctId: "user", Timestamp: timestamp}},
		{name: "group identify", msg: GroupIdentify{Uuid: "u", Type: "company", Key: "acme", Timestamp: timestamp}},
		{name: "alias", msg: Alias{Uuid: "u", DistinctId: "user", Alias: "anonymous", Timestamp: timestamp}},
		{name: "exception", msg: Exception{Uuid: "u", DistinctId: "user", Timestamp: timestamp, ExceptionList: []ExceptionItem{{Type: "error", Value: "boom"}}}},
	}

	for _, tc := range messages {
		t.Run(tc.name, func(t *testing.T) {
			data, _, _, err := prepareForSend(tc.msg, nil)
			if err != nil {
				t.Fatalf("prepareForSend: %v", err)
			}
			assertWireTimestamp(t, data, want)
		})
	}
}

func assertWireTimestamp(t *testing.T, data []byte, want string) {
	t.Helper()
	var payload struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", payload.Timestamp, want)
	}
}

func TestTimestampNormalizationDoesNotRewriteCallerProperties(t *testing.T) {
	callerTime := time.Date(2025, time.May, 6, 7, 8, 9, 0, time.FixedZone("UTC+5:30", 5*60*60+30*60))
	data, _, _, err := prepareForSend(Capture{
		Event:      "event",
		DistinctId: "user",
		Timestamp:  callerTime,
		Properties: Properties{"caller_time": callerTime},
	}, nil)
	if err != nil {
		t.Fatalf("prepareForSend: %v", err)
	}

	var payload struct {
		Timestamp  string            `json:"timestamp"`
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Timestamp != "2025-05-06T01:38:09.000Z" {
		t.Errorf("timestamp = %q, want UTC", payload.Timestamp)
	}
	if payload.Properties["caller_time"] != "2025-05-06T07:08:09+05:30" {
		t.Errorf("caller_time = %q, want original offset", payload.Properties["caller_time"])
	}
}

func TestDefaultEventTimestampsUseUTC(t *testing.T) {
	body, server := mockServer()
	defer server.Close()

	defaultTimestamp := time.Date(2025, time.March, 8, 9, 10, 11, 0, time.FixedZone("UTC+9", 9*60*60))
	client, err := NewWithConfig("test-key", Config{
		Endpoint:  server.URL,
		BatchSize: 5,
		now:       func() time.Time { return defaultTimestamp },
	})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	defer client.Close()

	messages := []Message{
		Capture{Event: "event", DistinctId: "user"},
		Identify{DistinctId: "user"},
		GroupIdentify{Type: "company", Key: "acme"},
		Alias{DistinctId: "user", Alias: "anonymous"},
		Exception{DistinctId: "user", ExceptionList: []ExceptionItem{{Type: "error", Value: "boom"}}},
	}
	for _, msg := range messages {
		if err := client.Enqueue(msg); err != nil {
			t.Fatalf("Enqueue(%T): %v", msg, err)
		}
	}

	var payload struct {
		Batch []json.RawMessage `json:"batch"`
	}
	if err := json.Unmarshal(<-body, &payload); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(payload.Batch) != len(messages) {
		t.Fatalf("batch length = %d, want %d", len(payload.Batch), len(messages))
	}
	for _, event := range payload.Batch {
		assertWireTimestamp(t, event, "2025-03-08T00:10:11.000Z")
	}
}

// Capture requires at least millisecond digits, so whole seconds keep ".000".
func TestWireTimestampsKeepAtLeastMilliseconds(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2025-06-07T08:09:10Z", "2025-06-07T08:09:10.000Z"},
		{"2025-06-07T08:09:10.000Z", "2025-06-07T08:09:10.000Z"},
		{"2025-06-07T08:09:10.120Z", "2025-06-07T08:09:10.120Z"},
		{"2025-06-07T08:09:10.000001Z", "2025-06-07T08:09:10.000001Z"},
		{"2025-06-07T08:09:10.1234Z", "2025-06-07T08:09:10.123400Z"},
		{"2025-06-07T08:09:10.000000001Z", "2025-06-07T08:09:10.000000001Z"},
		{"2025-06-07T08:09:10.1234567+02:00", "2025-06-07T06:09:10.123456700Z"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			ts, err := time.Parse(time.RFC3339Nano, tc.input)
			require.NoError(t, err)

			data, _, _, err := prepareForSend(Capture{Uuid: uuidA, Event: "event", DistinctId: "user", Timestamp: ts}, nil)
			require.NoError(t, err)
			assertWireTimestamp(t, data, tc.want)

			srv := &captureTestServer{respond: func(_ int, _ []string) (int, string, string) {
				return http.StatusOK, `{"results":{}}`, ""
			}}
			server := httptest.NewServer(srv.handler(t))
			defer server.Close()
			c := newCaptureTestClient(t, server.URL, nil, 0, func(cfg *Config) {
				cfg.now = func() time.Time { return ts }
			})
			c.send(c.analytics, captureBatch(t, cap1(uuidA)))

			reqs := srv.snapshot()
			require.Len(t, reqs, 1)
			require.Equal(t, tc.want, reqs[0].timestamp, "PostHog-Request-Timestamp")
			require.Equal(t, tc.want, reqs[0].createdAt, "created_at")
		})
	}
}

// A timestamp RFC 3339 cannot hold fails the event locally instead of reaching capture.
func TestEventTimestampOutsideRFC3339FailsLocally(t *testing.T) {
	for _, ts := range []time.Time{
		time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, time.January, 1, 0, 0, 0, 0, time.UTC),
	} {
		_, _, _, err := prepareForSend(Capture{Event: "event", DistinctId: "user", Timestamp: ts}, nil)
		require.Error(t, err, "%v", ts)
	}
}
