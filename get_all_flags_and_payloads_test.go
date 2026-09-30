package posthog_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/require"
)

func TestGetAllFlagsAndPayloadsRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"featureFlags": {"enabled-flag": true, "variant-flag": "control"},
				"featureFlagPayloads": {
					"enabled-flag": "{\"plan\":\"pro\"}",
					"variant-flag": "{\"variant\":1}"
				}
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, err := posthog.NewWithConfig("test-api-key", posthog.Config{Endpoint: server.URL})
	require.NoError(t, err)
	defer client.Close()

	got, err := client.GetAllFlagsAndPayloads(posthog.FeatureFlagPayloadNoKey{DistinctId: "user-1"})
	require.NoError(t, err)
	require.Equal(t, true, got.FeatureFlags["enabled-flag"])
	require.Equal(t, "control", got.FeatureFlags["variant-flag"])
	require.Equal(t, `{"plan":"pro"}`, got.FeatureFlagPayloads["enabled-flag"])
	require.Equal(t, `{"variant":1}`, got.FeatureFlagPayloads["variant-flag"])
}
