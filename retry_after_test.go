package posthog

import (
	"net/http"
	"testing"
	"time"
)

func TestLegacyRetryDelay(t *testing.T) {
	const base = 100 * time.Millisecond
	err := func(d time.Duration, has bool) *httpError {
		return &httpError{retryAfter: d, hasRetryAfter: has}
	}
	cases := []struct {
		name string
		http *httpError
		want time.Duration
	}{
		{"no_response_uses_configured", nil, base},
		{"no_retry_after_uses_configured", err(0, false), base},
		{"larger_retry_after_wins", err(5*time.Second, true), 5 * time.Second},
		{"smaller_retry_after_ignored", err(10*time.Millisecond, true), base},
		{"retry_after_at_ceiling", err(defaultMaxBackoff, true), defaultMaxBackoff},
		{"retry_after_above_ceiling_clamped", err(90*time.Second, true), defaultMaxBackoff},
	}
	c := &client{Config: Config{RetryAfter: func(int) time.Duration { return base }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.legacyRetryDelay(0, tc.http); got != tc.want {
				t.Errorf("legacyRetryDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name     string
		value    string
		wantDur  time.Duration
		wantOkay bool
	}{
		{
			name:     "seconds",
			value:    "5",
			wantDur:  5 * time.Second,
			wantOkay: true,
		},
		{
			name:     "seconds_with_spaces",
			value:    " 10 ",
			wantDur:  10 * time.Second,
			wantOkay: true,
		},
		{
			name:     "http_date",
			value:    now.Add(2 * time.Second).Format(http.TimeFormat),
			wantDur:  2 * time.Second,
			wantOkay: true,
		},
		{
			name:     "empty",
			value:    "",
			wantDur:  0,
			wantOkay: false,
		},
		{
			name:     "invalid",
			value:    "nope",
			wantDur:  0,
			wantOkay: false,
		},
		{
			name:     "zero",
			value:    "0",
			wantDur:  0,
			wantOkay: false,
		},
		{
			name:     "negative",
			value:    "-3",
			wantDur:  0,
			wantOkay: false,
		},
		{
			name:     "past_http_date",
			value:    now.Add(-2 * time.Second).Format(time.RFC1123),
			wantDur:  0,
			wantOkay: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotDur, gotOkay := parseRetryAfter(tc.value, now)
			if gotOkay != tc.wantOkay {
				t.Fatalf("expected ok=%v, got %v", tc.wantOkay, gotOkay)
			}
			if gotDur != tc.wantDur {
				t.Fatalf("expected duration=%v, got %v", tc.wantDur, gotDur)
			}
		})
	}
}

func TestIsRetryableStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "request_timeout", status: http.StatusRequestTimeout, want: true},
		{name: "too_many_requests", status: http.StatusTooManyRequests, want: true},
		{name: "internal_server_error", status: http.StatusInternalServerError, want: true},
		{name: "bad_gateway", status: http.StatusBadGateway, want: true},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, want: true},
		{name: "gateway_timeout", status: http.StatusGatewayTimeout, want: true},
		{name: "bad_request", status: http.StatusBadRequest, want: false},
		{name: "unauthorized", status: http.StatusUnauthorized, want: false},
		{name: "forbidden", status: http.StatusForbidden, want: false},
		{name: "payload_too_large", status: http.StatusRequestEntityTooLarge, want: false},
		{name: "not_found", status: http.StatusNotFound, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableStatus(tc.status)
			if got != tc.want {
				t.Fatalf("expected retryable=%v, got %v", tc.want, got)
			}
		})
	}
}
