package posthog

import (
	"net/http"
	"testing"
	"time"
)

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
