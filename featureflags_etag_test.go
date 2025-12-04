package posthog

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestPoller creates a FeatureFlagsPoller for testing without starting the background goroutine.
// This allows tests to call fetchNewFeatureFlags() directly for synchronous, deterministic testing.
func newTestPoller(t *testing.T, serverURL string) *FeatureFlagsPoller {
	t.Helper()

	localEvalURL, err := url.Parse(serverURL + "/api/feature_flag/local_evaluation")
	if err != nil {
		t.Fatalf("Failed to parse URL: %v", err)
	}

	return &FeatureFlagsPoller{
		personalApiKey: "test-personal-key",
		projectApiKey:  "test-api-key",
		localEvalUrl:   localEvalURL,
		Logger:         newDefaultLogger(false),
		Endpoint:       serverURL,
		http:           http.Client{},
		mutex:          sync.RWMutex{},
		flagTimeout:    10 * time.Second,
	}
}

func TestETagSupportForLocalEvaluation(t *testing.T) {
	t.Run("stores ETag from initial response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				w.Header().Set("ETag", `"abc123"`)
				w.Write([]byte(`{
					"flags": [{"key": "test-flag", "active": true, "filters": {"groups": []}}],
					"group_type_mapping": {},
					"cohorts": {}
				}`))
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag != `"abc123"` {
			t.Errorf("Expected ETag to be \"abc123\", got %q", etag)
		}
	})

	t.Run("sends If-None-Match header on subsequent requests", func(t *testing.T) {
		var requestCount int
		var receivedIfNoneMatch string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++
				receivedIfNoneMatch = r.Header.Get("If-None-Match")

				if requestCount == 1 {
					w.Header().Set("ETag", `"initial-etag"`)
					w.Write([]byte(`{
						"flags": [{"key": "test-flag", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					w.Header().Set("ETag", `"initial-etag"`)
					w.WriteHeader(http.StatusNotModified)
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		// First request - no If-None-Match
		poller.fetchNewFeatureFlags()
		if requestCount != 1 {
			t.Fatalf("Expected 1 request, got %d", requestCount)
		}

		// Second request - should include If-None-Match
		poller.fetchNewFeatureFlags()
		if requestCount != 2 {
			t.Fatalf("Expected 2 requests, got %d", requestCount)
		}

		if receivedIfNoneMatch != `"initial-etag"` {
			t.Errorf("Expected If-None-Match header to be \"initial-etag\", got %q", receivedIfNoneMatch)
		}
	})

	t.Run("handles 304 Not Modified response", func(t *testing.T) {
		var requestCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++

				if requestCount == 1 {
					w.Header().Set("ETag", `"test-etag"`)
					w.Write([]byte(`{
						"flags": [{"key": "cached-flag", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {"0": "company"},
						"cohorts": {}
					}`))
				} else {
					w.Header().Set("ETag", `"test-etag"`)
					w.WriteHeader(http.StatusNotModified)
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		// First request - get flags
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		flags := poller.featureFlags
		poller.mutex.RUnlock()

		if len(flags) != 1 || flags[0].Key != "cached-flag" {
			t.Errorf("Expected initial flag 'cached-flag', got %+v", flags)
		}

		// Second request - 304
		poller.fetchNewFeatureFlags()

		// Flags should still be available after 304
		poller.mutex.RLock()
		flags = poller.featureFlags
		poller.mutex.RUnlock()

		if len(flags) != 1 || flags[0].Key != "cached-flag" {
			t.Errorf("Expected flags to remain 'cached-flag' after 304, got %+v", flags)
		}
	})

	t.Run("updates ETag when flags change", func(t *testing.T) {
		var requestCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++

				if requestCount == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					w.Header().Set("ETag", `"etag-v2"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v2", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag1 := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Second request - new ETag
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag2 := poller.flagsEtag
		flags := poller.featureFlags
		poller.mutex.RUnlock()

		if etag2 != `"etag-v2"` {
			t.Errorf("Expected updated ETag to be \"etag-v2\", got %q", etag2)
		}
		if len(flags) != 1 || flags[0].Key != "flag-v2" {
			t.Errorf("Expected flag to be updated to 'flag-v2', got %+v", flags)
		}
	})

	t.Run("clears ETag when server stops sending it", func(t *testing.T) {
		var requestCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++

				if requestCount == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// No ETag in response
					w.Write([]byte(`{
						"flags": [{"key": "flag-v2", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag1 := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Second request - no ETag
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag2 := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag2 != "" {
			t.Errorf("Expected ETag to be cleared when server stops sending it, got %q", etag2)
		}
	})

	t.Run("preserves ETag when 304 response has no ETag header", func(t *testing.T) {
		var requestCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++

				if requestCount == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// 304 without ETag header (unusual but possible)
					w.WriteHeader(http.StatusNotModified)
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag1 := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Second request - 304 without ETag
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag2 := poller.flagsEtag
		flags := poller.featureFlags
		poller.mutex.RUnlock()

		if etag2 != `"etag-v1"` {
			t.Errorf("Expected ETag to be preserved when 304 has no ETag header, got %q", etag2)
		}
		if len(flags) != 1 || flags[0].Key != "flag-v1" {
			t.Errorf("Expected flags to remain after 304 without ETag, got %+v", flags)
		}
	})

	t.Run("clears ETag on quota limit (402)", func(t *testing.T) {
		var requestCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				requestCount++

				if requestCount == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					w.WriteHeader(http.StatusPaymentRequired)
					w.Write([]byte(`{
						"type": "quota_limited",
						"detail": "You have exceeded your feature flag request quota"
					}`))
				}
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)

		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag1 := poller.flagsEtag
		poller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Second request - quota limited
		poller.fetchNewFeatureFlags()

		poller.mutex.RLock()
		etag2 := poller.flagsEtag
		flags := poller.featureFlags
		poller.mutex.RUnlock()

		if etag2 != "" {
			t.Errorf("Expected ETag to be cleared on quota limit, got %q", etag2)
		}
		if len(flags) != 0 {
			t.Errorf("Expected flags to be cleared on quota limit, got %+v", flags)
		}
	})

	t.Run("first request does not send If-None-Match header", func(t *testing.T) {
		var firstRequestIfNoneMatch string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				firstRequestIfNoneMatch = r.Header.Get("If-None-Match")
				w.Header().Set("ETag", `"abc123"`)
				w.Write([]byte(`{
					"flags": [],
					"group_type_mapping": {},
					"cohorts": {}
				}`))
			}
		}))
		defer server.Close()

		poller := newTestPoller(t, server.URL)
		poller.fetchNewFeatureFlags()

		if firstRequestIfNoneMatch != "" {
			t.Errorf("Expected no If-None-Match header on first request, got %q", firstRequestIfNoneMatch)
		}
	})
}
