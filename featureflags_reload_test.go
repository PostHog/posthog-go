package posthog

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReloadFeatureFlagsAfterCloseDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flags":[],"group_type_mapping":{}}`))
	}))
	defer server.Close()

	client, err := NewWithConfig("phc_test", Config{
		SecretKey:                          "phs_test",
		Endpoint:                           server.URL,
		Interval:                           time.Hour,
		DefaultFeatureFlagsPollingInterval: time.Hour,
	})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.NotPanics(t, func() {
		err = client.ReloadFeatureFlags()
	})
	require.ErrorIs(t, err, ErrClosed)
}

func TestReloadFeatureFlagsOverlappingClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flags":[],"group_type_mapping":{}}`))
	}))
	defer server.Close()

	client, err := NewWithConfig("phc_test", Config{
		SecretKey:                          "phs_test",
		Endpoint:                           server.URL,
		Interval:                           time.Hour,
		DefaultFeatureFlagsPollingInterval: time.Hour,
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = client.ReloadFeatureFlags()
		}
	}()
	require.NoError(t, client.Close())
	wg.Wait()
	require.ErrorIs(t, client.ReloadFeatureFlags(), ErrClosed)
}
