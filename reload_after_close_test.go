package posthog

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReloadFeatureFlagsAfterClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flags":[]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewWithConfig("phc_test", Config{
		SecretKey: "phs_test",
		Endpoint:  server.URL,
	})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.NotPanics(t, func() {
		err = client.ReloadFeatureFlags()
	})
	require.ErrorIs(t, err, ErrClosed)
}

func TestReloadFeatureFlagsDuringClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flags":[]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewWithConfig("phc_test", Config{
		SecretKey: "phs_test",
		Endpoint:  server.URL,
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = client.ReloadFeatureFlags()
			}
		}()
	}
	require.NotPanics(t, func() {
		require.NoError(t, client.Close())
	})
	wg.Wait()
	require.ErrorIs(t, client.ReloadFeatureFlags(), ErrClosed)
}
