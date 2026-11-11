package posthog

import (
	"testing"
	"time"
)

func TestClampRetryWait(t *testing.T) {
	const configured = time.Millisecond

	if got := clampRetryWait(configured, 0, false); got != configured {
		t.Fatalf("no header: got %s, want %s", got, configured)
	}
	if got := clampRetryWait(configured, 2*time.Second, true); got != 2*time.Second {
		t.Fatalf("header above configured: got %s", got)
	}
	if got := clampRetryWait(configured, 2*time.Minute, true); got != defaultMaxBackoff {
		t.Fatalf("header above ceiling: got %s, want %s", got, defaultMaxBackoff)
	}
	// A caller-configured wait longer than the ceiling is not truncated.
	if got := clampRetryWait(45*time.Second, 2*time.Minute, true); got != 45*time.Second {
		t.Fatalf("configured backoff truncated: got %s", got)
	}
}
