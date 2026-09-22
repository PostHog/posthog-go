package posthog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFlagDefinitionCacheInvalidDefinitions(t *testing.T) {
	for _, body := range []string{"", `{`, `null`, `[]`, `{}`, `{"flags":null}`, `{"flags":[null]}`, `{"flags":[{}]}`} {
		for _, mode := range []string{"cold", "warm"} {
			t.Run(mode+"/"+body, func(t *testing.T) {
				provider := &fakeFlagDefinitionCache{shouldFetch: mode == "warm"}
				server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))
				poller := newCachingTestPoller(t, server.URL, provider)
				if mode == "warm" {
					poller.fetchNewFeatureFlags()
				}
				previous := poller.state.Load()

				provider.shouldFetch = false
				provider.cached = json.RawMessage(body)
				poller.fetchNewFeatureFlags()

				require.Equal(t, 1, requests())
				state := poller.state.Load()
				require.NotNil(t, state)
				require.Contains(t, state.flagsByKey, "cached-flag")
				require.NotContains(t, state.flagsByKey, "")
				_, _, _, published := provider.calls()
				if mode == "warm" {
					require.Same(t, previous, state, "invalid cache data must retain the whole snapshot")
					require.Len(t, published, 1)
				} else {
					require.Empty(t, published, "a follower's recovery fetch must not publish")
				}
			})
		}
	}
}

func TestFlagDefinitionCacheCancelledRefreshDoesNotCallProvider(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))
	poller := newCachingTestPoller(t, server.URL, provider)
	poller.cancel()
	poller.fetchNewFeatureFlags()

	shouldFetch, get, _, published := provider.calls()
	require.Zero(t, shouldFetch)
	require.Zero(t, get)
	require.Empty(t, published)
	require.Zero(t, requests())
}

func TestFlagDefinitionCacheCloseDeadlineReturnsBeforeProviderCleanup(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	var releaseOnce sync.Once
	var active atomic.Bool
	var overlap atomic.Bool

	provider := &fakeFlagDefinitionCache{
		shouldFetch: false,
		onShouldFetch: func(ctx context.Context) {
			active.Store(true)
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			active.Store(false)
		},
		onShutdown: func(context.Context) error {
			overlap.Store(active.Load())
			close(stopped)
			return nil
		},
	}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))
	cli, err := NewWithConfig("phc_test", Config{
		Endpoint:                           server.URL,
		SecretKey:                          "phs_test",
		DefaultFeatureFlagsPollingInterval: time.Hour,
		FlagDefinitionCacheProvider:        provider,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = cli.CloseWithContext(ctx)
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- cli.CloseWithContext(ctx) }()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive cancellation")
	}
	select {
	case err := <-closed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("CloseWithContext is waiting for provider cleanup after its deadline")
	}
	select {
	case <-stopped:
		t.Fatal("Shutdown ran before the active provider operation returned")
	default:
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not shut down after its operation returned")
	}
	require.False(t, overlap.Load())
	shouldFetch, get, shutdown, published := provider.calls()
	require.Equal(t, 1, shouldFetch)
	require.Zero(t, get)
	require.Equal(t, 1, shutdown)
	require.Empty(t, published)
	require.Zero(t, requests())
}

func TestFlagDefinitionCacheMatchingVersionFromCache(t *testing.T) {
	provider := &fakeFlagDefinitionCache{}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))
	poller := newCachingTestPoller(t, server.URL, provider)
	for _, step := range []struct {
		version int
		want    bool
	}{{2, false}, {0, true}, {2, false}, {1, true}, {3, true}} {
		versionField := ""
		if step.version != 0 {
			versionField = fmt.Sprintf(`,"property_matching_version":%d`, step.version)
		}
		provider.cached = json.RawMessage(fmt.Sprintf(matchingVersionDefinitions, versionField))
		poller.fetchNewFeatureFlags()
		value, local, err := poller.GetFeatureFlag(FeatureFlagPayload{
			Key:                 "person",
			DistinctId:          "user-1",
			PersonProperties:    Properties{"value": "banana"},
			OnlyEvaluateLocally: true,
		})
		require.NoError(t, err)
		require.True(t, local)
		require.Equal(t, step.want, value, "matching version %d", step.version)
		require.Equal(t, step.version, poller.state.Load().propertyMatchingVersion)
	}
	require.Zero(t, requests())
}

func TestFlagDefinitionCacheUnknownMetadataSurvivesRepublishing(t *testing.T) {
	const payload = `{
		"flags": [],
		"group_type_mapping": {},
		"cohorts": {},
		"minimal_flag_called_events": true,
		"property_matching_version": 2,
		"future_metadata": {"large_integer": 9007199254740993, "nested": [null, true, {"key":"value"}]}
	}`
	server, requests := definitionsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"metadata"`)
		if r.Header.Get("If-None-Match") == `"metadata"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(payload))
	})
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()
	poller.fetchNewFeatureFlags()

	_, _, _, published := provider.calls()
	require.Equal(t, 2, requests())
	require.Len(t, published, 2)
	for _, data := range published {
		require.Equal(t, payload, string(data))

		follower := &fakeFlagDefinitionCache{cached: data}
		followerPoller := newCachingTestPoller(t, server.URL, follower)
		followerPoller.fetchNewFeatureFlags()
		require.Equal(t, 2, requests(), "hydration must use the cache")
		state := followerPoller.state.Load()
		require.Equal(t, 2, state.propertyMatchingVersion)
		require.True(t, state.minimalFlagCalledEvents)
		require.Equal(t, payload, string(state.definitions))
	}
}
