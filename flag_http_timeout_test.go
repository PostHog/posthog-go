package posthog

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFeatureFlagRequestTimeoutNotCappedByBatchUploadTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags/" {
			time.Sleep(3 * time.Second)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"featureFlags":{"k":true},"featureFlagPayloads":{}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, err := NewWithConfig("phc_test", Config{
		Endpoint:                  server.URL,
		BatchUploadTimeout:        500 * time.Millisecond,
		FeatureFlagRequestTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	defer client.Close()

	start := time.Now()
	enabled, err := client.IsFeatureEnabled(FeatureFlagPayload{
		Key:        "k",
		DistinctId: "user-1",
	})
	require.NoError(t, err)
	require.Equal(t, true, enabled)
	require.Greater(t, time.Since(start), 2500*time.Millisecond)
	require.Less(t, time.Since(start), 4500*time.Millisecond)
}
