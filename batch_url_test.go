package posthog

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyBatchURL(t *testing.T) {
	u, err := legacyBatchURL("https://app.posthog.com", false)
	require.NoError(t, err)
	require.Equal(t, "https://app.posthog.com/batch/", u)

	u, err = legacyBatchURL("https://app.posthog.com", true)
	require.NoError(t, err)
	require.Equal(t, "https://app.posthog.com/batch/?compression=gzip", u)

	u, err = legacyBatchURL("https://proxy.example.com?token=abc", true)
	require.NoError(t, err)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, "/batch/", parsed.Path)
	require.Equal(t, "abc", parsed.Query().Get("token"))
	require.Equal(t, "gzip", parsed.Query().Get("compression"))
}
