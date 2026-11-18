package posthog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func reloadFlagDefinitions(rollout int) string {
	return fmt.Sprintf(`{"flags":[{"id":1,"key":"reload-flag","active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":%d}]}}]}`, rollout)
}

// reloadResponses is what the definitions endpoint answers. A status of -1 holds
// the request until the test ends.
type reloadResponses struct {
	body   atomic.Value
	status atomic.Int64
	hold   chan struct{}
}

func newReloadServer(t *testing.T, body string) (*reloadResponses, string, func() int) {
	responses := &reloadResponses{hold: make(chan struct{})}
	responses.body.Store(body)
	responses.status.Store(http.StatusOK)
	server, requests := definitionsServer(t, func(w http.ResponseWriter, r *http.Request) {
		status := int(responses.status.Load())
		if status == -1 {
			<-responses.hold
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responses.body.Load().(string)))
	})
	t.Cleanup(func() { close(responses.hold) })
	return responses, server.URL, requests
}

func newReloadClient(t *testing.T, endpoint string) Client {
	client, err := NewWithConfig("phc_test", Config{
		SecretKey:                          "phs_test",
		Endpoint:                           endpoint,
		DefaultFeatureFlagsPollingInterval: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func localReloadFlag(t *testing.T, client Client) interface{} {
	value, err := client.GetFeatureFlag(FeatureFlagPayload{Key: "reload-flag", DistinctId: "user-1", OnlyEvaluateLocally: true})
	require.NoError(t, err)
	return value
}

func TestReloadFeatureFlagsWithContextUsesFreshDefinitionsOnReturn(t *testing.T) {
	responses, endpoint, requests := newReloadServer(t, reloadFlagDefinitions(0))
	client := newReloadClient(t, endpoint)
	reloader, ok := client.(FlagReloader)
	require.True(t, ok, "the client implements FlagReloader")

	require.NoError(t, reloader.ReloadFeatureFlagsWithContext(context.Background()))
	require.Equal(t, false, localReloadFlag(t, client))

	responses.body.Store(reloadFlagDefinitions(100))
	before := requests()
	require.NoError(t, reloader.ReloadFeatureFlagsWithContext(context.Background()))
	require.Greater(t, requests(), before, "the reload made a fresh definitions request")
	require.Equal(t, true, localReloadFlag(t, client), "the new definitions are in use when the call returns")
}

func TestReloadFeatureFlagsWithContextReportsFailedLoads(t *testing.T) {
	responses, endpoint, _ := newReloadServer(t, reloadFlagDefinitions(100))
	reloader := newReloadClient(t, endpoint).(FlagReloader)
	require.NoError(t, reloader.ReloadFeatureFlagsWithContext(context.Background()))

	responses.status.Store(http.StatusInternalServerError)
	require.ErrorContains(t, reloader.ReloadFeatureFlagsWithContext(context.Background()), "status 500")

	responses.status.Store(http.StatusPaymentRequired)
	require.ErrorIs(t, reloader.ReloadFeatureFlagsWithContext(context.Background()), ErrFlagDefinitionsQuotaLimited)
}

func TestReloadFeatureFlagsWithContextStopsWaitingWhenTheContextEnds(t *testing.T) {
	responses, endpoint, _ := newReloadServer(t, reloadFlagDefinitions(100))
	reloader := newReloadClient(t, endpoint).(FlagReloader)
	require.NoError(t, reloader.ReloadFeatureFlagsWithContext(context.Background()))

	responses.status.Store(-1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, reloader.ReloadFeatureFlagsWithContext(ctx), context.DeadlineExceeded)
}

func TestReloadFeatureFlagsWithContextNeedsASecretKeyAndAnOpenClient(t *testing.T) {
	noKey, err := NewWithConfig("phc_test", Config{Endpoint: "http://127.0.0.1:1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = noKey.Close() })
	require.ErrorIs(t, noKey.(FlagReloader).ReloadFeatureFlagsWithContext(context.Background()), ErrNoSecretKey)

	_, endpoint, _ := newReloadServer(t, reloadFlagDefinitions(100))
	closed, err := NewWithConfig("phc_test", Config{SecretKey: "phs_test", Endpoint: endpoint})
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	require.ErrorIs(t, closed.(FlagReloader).ReloadFeatureFlagsWithContext(context.Background()), ErrClosed)
}
