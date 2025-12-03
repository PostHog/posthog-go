package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitForCondition polls until condition returns true or timeout expires.
// Returns true if condition was met, false if timed out.
func waitForCondition(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
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

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		// Verify ETag is stored
		c.featureFlagsPoller.mutex.RLock()
		etag := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag != `"abc123"` {
			t.Errorf("Expected ETag to be \"abc123\", got %q", etag)
		}
	})

	t.Run("sends If-None-Match header on subsequent requests", func(t *testing.T) {
		var requestCount int32
		var receivedIfNoneMatch atomic.Value
		receivedIfNoneMatch.Store("")

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				receivedIfNoneMatch.Store(r.Header.Get("If-None-Match"))

				if count == 1 {
					// First request - return flags with ETag
					w.Header().Set("ETag", `"initial-etag"`)
					w.Write([]byte(`{
						"flags": [{"key": "test-flag", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// Subsequent requests - return 304 if ETag matches
					if r.Header.Get("If-None-Match") == `"initial-etag"` {
						w.Header().Set("ETag", `"initial-etag"`)
						w.WriteHeader(http.StatusNotModified)
					} else {
						w.Header().Set("ETag", `"new-etag"`)
						w.Write([]byte(`{
							"flags": [{"key": "test-flag-v2", "active": true, "filters": {"groups": []}}],
							"group_type_mapping": {},
							"cohorts": {}
						}`))
					}
				}
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey:            "test-personal-key",
			Endpoint:                  server.URL,
			FeatureFlagRequestTimeout: 1 * time.Second,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		// Force a reload to trigger a second request
		c.featureFlagsPoller.ForceReload()

		// Wait for second request to complete
		if !waitForCondition(2*time.Second, func() bool {
			return atomic.LoadInt32(&requestCount) >= 2
		}) {
			t.Fatal("Timed out waiting for second request")
		}

		ifNoneMatch := receivedIfNoneMatch.Load().(string)
		if ifNoneMatch != `"initial-etag"` {
			t.Errorf("Expected If-None-Match header to be \"initial-etag\", got %q", ifNoneMatch)
		}
	})

	t.Run("handles 304 Not Modified response", func(t *testing.T) {
		var requestCount int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				if count == 1 {
					// First request - return flags with ETag
					w.Header().Set("ETag", `"test-etag"`)
					w.Write([]byte(`{
						"flags": [{"key": "cached-flag", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {"0": "company"},
						"cohorts": {}
					}`))
				} else {
					// Second request - return 304 Not Modified
					w.Header().Set("ETag", `"test-etag"`)
					w.WriteHeader(http.StatusNotModified)
				}
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		flags, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}
		if len(flags) != 1 || flags[0].Key != "cached-flag" {
			t.Errorf("Expected initial flag 'cached-flag', got %+v", flags)
		}

		// Force a reload - should get 304
		c.featureFlagsPoller.ForceReload()

		// Wait for second request to complete
		if !waitForCondition(2*time.Second, func() bool {
			return atomic.LoadInt32(&requestCount) >= 2
		}) {
			t.Fatal("Timed out waiting for second request")
		}

		// Flags should still be the same after 304
		flags, err = cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags after 304: %v", err)
		}
		if len(flags) != 1 || flags[0].Key != "cached-flag" {
			t.Errorf("Expected flags to remain 'cached-flag' after 304, got %+v", flags)
		}
	})

	t.Run("updates ETag when flags change", func(t *testing.T) {
		var requestCount int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				if count == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// Second request - flags changed, new ETag
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

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		c.featureFlagsPoller.mutex.RLock()
		etag1 := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Force reload to get new flags
		c.featureFlagsPoller.ForceReload()

		// Wait for ETag to update
		if !waitForCondition(2*time.Second, func() bool {
			c.featureFlagsPoller.mutex.RLock()
			etag := c.featureFlagsPoller.flagsEtag
			c.featureFlagsPoller.mutex.RUnlock()
			return etag == `"etag-v2"`
		}) {
			c.featureFlagsPoller.mutex.RLock()
			etag := c.featureFlagsPoller.flagsEtag
			c.featureFlagsPoller.mutex.RUnlock()
			t.Errorf("Expected updated ETag to be \"etag-v2\", got %q", etag)
		}

		// Verify flags were updated
		flags, _ := cli.GetFeatureFlags()
		if len(flags) != 1 || flags[0].Key != "flag-v2" {
			t.Errorf("Expected flag to be updated to 'flag-v2', got %+v", flags)
		}
	})

	t.Run("clears ETag when server stops sending it", func(t *testing.T) {
		var requestCount int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				if count == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// Second request - no ETag in response
					w.Write([]byte(`{
						"flags": [{"key": "flag-v2", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				}
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		c.featureFlagsPoller.mutex.RLock()
		etag1 := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Force reload - server won't send ETag
		c.featureFlagsPoller.ForceReload()

		// Wait for ETag to be cleared
		if !waitForCondition(2*time.Second, func() bool {
			c.featureFlagsPoller.mutex.RLock()
			etag := c.featureFlagsPoller.flagsEtag
			c.featureFlagsPoller.mutex.RUnlock()
			return etag == ""
		}) {
			c.featureFlagsPoller.mutex.RLock()
			etag := c.featureFlagsPoller.flagsEtag
			c.featureFlagsPoller.mutex.RUnlock()
			t.Errorf("Expected ETag to be cleared when server stops sending it, got %q", etag)
		}
	})

	t.Run("preserves ETag when 304 response has no ETag header", func(t *testing.T) {
		var requestCount int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				if count == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// Second request - 304 without ETag header (unusual but possible)
					w.WriteHeader(http.StatusNotModified)
				}
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		c.featureFlagsPoller.mutex.RLock()
		etag1 := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Force reload - server returns 304 without ETag header
		c.featureFlagsPoller.ForceReload()

		// Wait for second request to complete
		if !waitForCondition(2*time.Second, func() bool {
			return atomic.LoadInt32(&requestCount) >= 2
		}) {
			t.Fatal("Timed out waiting for second request")
		}

		// ETag should be preserved when 304 has no ETag header
		c.featureFlagsPoller.mutex.RLock()
		etag2 := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag2 != `"etag-v1"` {
			t.Errorf("Expected ETag to be preserved when 304 has no ETag header, got %q", etag2)
		}

		// Flags should still be available
		flags, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags after 304: %v", err)
		}
		if len(flags) != 1 || flags[0].Key != "flag-v1" {
			t.Errorf("Expected flags to remain after 304 without ETag, got %+v", flags)
		}
	})

	t.Run("clears ETag on quota limit (402)", func(t *testing.T) {
		var requestCount int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				count := atomic.AddInt32(&requestCount, 1)

				if count == 1 {
					w.Header().Set("ETag", `"etag-v1"`)
					w.Write([]byte(`{
						"flags": [{"key": "flag-v1", "active": true, "filters": {"groups": []}}],
						"group_type_mapping": {},
						"cohorts": {}
					}`))
				} else {
					// Second request - quota limited
					w.WriteHeader(http.StatusPaymentRequired)
					w.Write([]byte(`{
						"type": "quota_limited",
						"detail": "You have exceeded your feature flag request quota"
					}`))
				}
			} else if strings.HasPrefix(r.URL.Path, "/flags") {
				w.Write([]byte(`{"featureFlags": {}, "featureFlagPayloads": {}}`))
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		c := cli.(*client)

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		c.featureFlagsPoller.mutex.RLock()
		etag1 := c.featureFlagsPoller.flagsEtag
		c.featureFlagsPoller.mutex.RUnlock()

		if etag1 != `"etag-v1"` {
			t.Errorf("Expected initial ETag to be \"etag-v1\", got %q", etag1)
		}

		// Force reload - should get quota limited
		c.featureFlagsPoller.ForceReload()

		// Wait for ETag and flags to be cleared
		if !waitForCondition(2*time.Second, func() bool {
			c.featureFlagsPoller.mutex.RLock()
			etag := c.featureFlagsPoller.flagsEtag
			flags := c.featureFlagsPoller.featureFlags
			c.featureFlagsPoller.mutex.RUnlock()
			return etag == "" && len(flags) == 0
		}) {
			c.featureFlagsPoller.mutex.RLock()
			etag2 := c.featureFlagsPoller.flagsEtag
			flags := c.featureFlagsPoller.featureFlags
			c.featureFlagsPoller.mutex.RUnlock()

			if etag2 != "" {
				t.Errorf("Expected ETag to be cleared on quota limit, got %q", etag2)
			}
			if len(flags) != 0 {
				t.Errorf("Expected flags to be cleared on quota limit, got %+v", flags)
			}
		}
	})

	t.Run("first request does not send If-None-Match header", func(t *testing.T) {
		var firstRequestIfNoneMatch atomic.Value
		firstRequestIfNoneMatch.Store("")
		var requestReceived int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/feature_flag/local_evaluation") {
				if atomic.CompareAndSwapInt32(&requestReceived, 0, 1) {
					firstRequestIfNoneMatch.Store(r.Header.Get("If-None-Match"))
				}

				w.Header().Set("ETag", `"abc123"`)
				w.Write([]byte(`{
					"flags": [],
					"group_type_mapping": {},
					"cohorts": {}
				}`))
			}
		}))
		defer server.Close()

		cli, _ := NewWithConfig("test-api-key", Config{
			PersonalApiKey: "test-personal-key",
			Endpoint:       server.URL,
		})
		defer cli.Close()

		// GetFeatureFlags blocks until initial fetch completes
		_, err := cli.GetFeatureFlags()
		if err != nil {
			t.Fatalf("Failed to get feature flags: %v", err)
		}

		ifNoneMatch := firstRequestIfNoneMatch.Load().(string)
		if ifNoneMatch != "" {
			t.Errorf("Expected no If-None-Match header on first request, got %q", ifNoneMatch)
		}
	})
}
