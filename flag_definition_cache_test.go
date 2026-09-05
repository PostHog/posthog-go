package posthog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const cachedFlagDefinitions = `{
	"flags": [
		{
			"key": "cached-flag",
			"active": true,
			"filters": {
				"groups": [{"properties": [], "rollout_percentage": 100}],
				"payloads": {"true": "{\"from\":\"cache\"}"}
			}
		},
		{
			"key": "cached-multivariate",
			"active": true,
			"filters": {
				"groups": [{"properties": [{"key": "id", "operator": "exact", "value": "1", "type": "cohort"}], "rollout_percentage": 100}],
				"multivariate": {"variants": [{"key": "control", "name": "Control", "rollout_percentage": 100}]}
			}
		}
	],
	"group_type_mapping": {"0": "company"},
	"cohorts": {
		"1": {
			"type": "OR",
			"values": [{"key": "plan", "operator": "exact", "value": ["enterprise"], "type": "person"}]
		}
	},
	"minimal_flag_called_events": true
}`

type fakeFlagDefinitionCache struct {
	mu sync.Mutex

	shouldFetch    bool
	shouldFetchErr error
	cached         *FlagDefinitionCacheData
	getErr         error
	publishErr     error
	shutdownErr    error
	onShutdown     func(ctx context.Context) error

	shouldFetchCalls int
	getCalls         int
	published        []FlagDefinitionCacheData
	shutdownCalls    int
}

func (c *fakeFlagDefinitionCache) ShouldFetchFlagDefinitions(context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shouldFetchCalls++
	return c.shouldFetch, c.shouldFetchErr
}

func (c *fakeFlagDefinitionCache) GetFlagDefinitions(context.Context) (*FlagDefinitionCacheData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	return c.cached, c.getErr
}

func (c *fakeFlagDefinitionCache) OnFlagDefinitionsReceived(_ context.Context, data FlagDefinitionCacheData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published = append(c.published, data)
	return c.publishErr
}

func (c *fakeFlagDefinitionCache) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.shutdownCalls++
	onShutdown := c.onShutdown
	err := c.shutdownErr
	c.mu.Unlock()

	if onShutdown != nil {
		return onShutdown(ctx)
	}
	return err
}

func (c *fakeFlagDefinitionCache) calls() (shouldFetch, get, shutdown int, published []FlagDefinitionCacheData) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shouldFetchCalls, c.getCalls, c.shutdownCalls, append([]FlagDefinitionCacheData(nil), c.published...)
}

func definitionsServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, func() int) {
	t.Helper()

	var mu sync.Mutex
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.WriteHeader(http.StatusOK)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}
}

func serveDefinitions(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"etag-1"`)
		_, _ = w.Write([]byte(body))
	}
}

// newCachingTestPoller builds a poller wired to a cache provider without starting the
// polling goroutine.
func newCachingTestPoller(t *testing.T, serverURL string, provider FlagDefinitionCacheProvider) *FeatureFlagsPoller {
	t.Helper()

	poller := newTestPoller(t, serverURL)
	poller.cacheProvider = provider

	poller.firstFeatureFlagRequestFinished = make(chan bool)
	close(poller.firstFeatureFlagRequestFinished)

	poller.pollLoopDone = make(chan struct{})
	close(poller.pollLoopDone)

	return poller
}

func TestFlagDefinitionCacheLeaderFetchesAndPublishes(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests(), "the elected instance fetches from the API")

	shouldFetchCalls, getCalls, _, published := provider.calls()
	require.Equal(t, 1, shouldFetchCalls)
	require.Zero(t, getCalls, "the fetching instance has no reason to read the cache")
	require.Len(t, published, 1, "fetched definitions are published for the other instances")

	require.Len(t, published[0].Flags, 2)
	require.Equal(t, map[string]string{"0": "company"}, published[0].GroupTypeMapping)
	require.Contains(t, published[0].Cohorts, "1")
	require.True(t, published[0].MinimalFlagCalledEvents)

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
	require.Equal(t, `"etag-1"`, state.flagsEtag)
}

func TestFlagDefinitionCacheFollowerReadsCacheInsteadOfAPI(t *testing.T) {
	var cached FlagDefinitionCacheData
	require.NoError(t, json.Unmarshal([]byte(cachedFlagDefinitions), &cached))

	provider := &fakeFlagDefinitionCache{shouldFetch: false, cached: &cached}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Zero(t, requests(), "a follower must not call the API")

	shouldFetchCalls, getCalls, _, published := provider.calls()
	require.Equal(t, 1, shouldFetchCalls)
	require.Equal(t, 1, getCalls)
	require.Empty(t, published, "a follower must not publish definitions it did not fetch")

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
	require.Equal(t, map[string]string{"0": "company"}, state.groups)
	require.True(t, state.minimalFlagCalledEvents, "the gate travels with the cached definitions")
	require.Empty(t, state.flagsEtag, "cached definitions must not inherit an ETag")

	require.Contains(t, state.flagsByKey, "cached-flag")
	require.Equal(t, `{"from":"cache"}`, state.flagsByKey["cached-flag"].Filters.DecodedPayloads["true"])
	require.Len(t, state.flagsByKey["cached-multivariate"].Filters.VariantLookupTable, 1)
	require.Len(t, state.cohorts["1"].ParsedValues, 1)
}

func TestFlagDefinitionCacheEvaluatesFlagsLoadedFromCache(t *testing.T) {
	leader := &fakeFlagDefinitionCache{shouldFetch: true}
	server, _ := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	leaderPoller := newCachingTestPoller(t, server.URL, leader)
	leaderPoller.fetchNewFeatureFlags()

	_, _, _, published := leader.calls()
	require.Len(t, published, 1)

	encoded, err := json.Marshal(published[0])
	require.NoError(t, err)
	var roundTripped FlagDefinitionCacheData
	require.NoError(t, json.Unmarshal(encoded, &roundTripped))

	follower := &fakeFlagDefinitionCache{shouldFetch: false, cached: &roundTripped}
	followerPoller := newCachingTestPoller(t, server.URL, follower)
	followerPoller.fetchNewFeatureFlags()

	for name, poller := range map[string]*FeatureFlagsPoller{"leader": leaderPoller, "follower": followerPoller} {
		value, isLocal, err := poller.GetFeatureFlag(FeatureFlagPayload{
			Key:                 "cached-flag",
			DistinctId:          "user-1",
			OnlyEvaluateLocally: true,
		})
		require.NoError(t, err, name)
		require.True(t, isLocal, name)
		require.Equal(t, true, value, name)

		payload, err := poller.GetFeatureFlagPayload(FeatureFlagPayload{
			Key:                 "cached-flag",
			DistinctId:          "user-1",
			OnlyEvaluateLocally: true,
		})
		require.NoError(t, err, name)
		require.Equal(t, `{"from":"cache"}`, payload, name)

		variant, isLocal, err := poller.GetFeatureFlag(FeatureFlagPayload{
			Key:                 "cached-multivariate",
			DistinctId:          "user-1",
			PersonProperties:    Properties{"plan": "enterprise"},
			OnlyEvaluateLocally: true,
		})
		require.NoError(t, err, name)
		require.True(t, isLocal, name)
		require.Equal(t, "control", variant, name)
	}
}

func TestFlagDefinitionCacheEmptyFlagsIsAHit(t *testing.T) {
	provider := &fakeFlagDefinitionCache{
		shouldFetch: false,
		cached:      &FlagDefinitionCacheData{Flags: []FeatureFlag{}},
	}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Zero(t, requests(), "empty definitions from the cache are a hit, not a miss")

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Empty(t, state.featureFlags)
}

func TestFlagDefinitionCacheMissingFlagsIsAMiss(t *testing.T) {
	// What a provider that deserialized the JSON document `{}` hands back.
	provider := &fakeFlagDefinitionCache{shouldFetch: false, cached: &FlagDefinitionCacheData{}}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests(), "definitions without a flags key are unusable, so fetch instead")

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheUnusableDefinitionsKeepTheLoadedOnes(t *testing.T) {
	malformed := FlagDefinitionCacheData{
		Flags: []FeatureFlag{{
			Key:    "malformed-multivariate",
			Active: true,
			Filters: Filter{
				Multivariate: &Variants{Variants: []FlagVariant{{Key: "control"}}},
			},
		}},
	}

	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()
	require.Equal(t, 1, requests())

	provider.mu.Lock()
	provider.shouldFetch = false
	provider.cached = &malformed
	provider.mu.Unlock()

	require.NotPanics(t, poller.fetchNewFeatureFlags)

	require.Equal(t, 1, requests(), "warm definitions are kept, so there is nothing to recover")
	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
	require.Contains(t, state.flagsByKey, "cached-flag")
}

func TestFlagDefinitionCacheUnusableDefinitionsFetchWithoutWarmState(t *testing.T) {
	provider := &fakeFlagDefinitionCache{
		shouldFetch: false,
		cached: &FlagDefinitionCacheData{
			Flags: []FeatureFlag{{
				Key:     "malformed-multivariate",
				Active:  true,
				Filters: Filter{Multivariate: &Variants{Variants: []FlagVariant{{Key: "control"}}}},
			}},
		},
	}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	require.NotPanics(t, poller.fetchNewFeatureFlags)

	require.Equal(t, 1, requests())
	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheMissWithoutDefinitionsFetchesAnyway(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: false, cached: nil}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests())

	_, getCalls, _, published := provider.calls()
	require.Equal(t, 1, getCalls)
	require.Empty(t, published, "an instance that was told not to fetch must not publish")

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheMissKeepsDefinitionsAlreadyLoaded(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()
	require.Equal(t, 1, requests())

	provider.mu.Lock()
	provider.shouldFetch = false
	provider.cached = nil
	provider.mu.Unlock()

	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests(), "stale definitions are preferred over ignoring the cache provider")
	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheShouldFetchErrorFallsBackToAPI(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: false, shouldFetchErr: errors.New("redis down")}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests())

	_, getCalls, _, published := provider.calls()
	require.Zero(t, getCalls)
	require.Len(t, published, 1, "the fallback fetch is treated as a normal fetch")

	state := poller.state.Load()
	require.NotNil(t, state)
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheGetErrorIsTreatedAsAMiss(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: false, getErr: errors.New("redis down")}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)

	poller.fetchNewFeatureFlags()
	require.Equal(t, 1, requests())
	require.NotNil(t, poller.state.Load())

	poller.fetchNewFeatureFlags()
	require.Equal(t, 1, requests())
	require.Len(t, poller.state.Load().featureFlags, 2)
}

func TestFlagDefinitionCachePublishErrorKeepsDefinitionsInMemory(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true, publishErr: errors.New("redis down")}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests())
	state := poller.state.Load()
	require.NotNil(t, state, "a cache that cannot store definitions does not stop this instance")
	require.Len(t, state.featureFlags, 2)
}

func TestFlagDefinitionCacheNotModifiedRepublishesDefinitions(t *testing.T) {
	var requestCount int
	server, requests := definitionsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("ETag", `"etag-1"`)
		if requestCount == 1 {
			_, _ = w.Write([]byte(cachedFlagDefinitions))
			return
		}
		require.Equal(t, `"etag-1"`, r.Header.Get("If-None-Match"))
		w.WriteHeader(http.StatusNotModified)
	})

	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	poller := newCachingTestPoller(t, server.URL, provider)

	poller.fetchNewFeatureFlags()
	poller.fetchNewFeatureFlags()

	require.Equal(t, 2, requests())

	_, _, _, published := provider.calls()
	require.Len(t, published, 2)
	require.Len(t, published[1].Flags, 2, "the 304 republishes the definitions in memory")
	require.Contains(t, published[1].Cohorts, "1")
	require.True(t, published[1].MinimalFlagCalledEvents, "the 304 republishes the gate too")
}

func TestFlagDefinitionCacheNotModifiedDoesNotPublishForFollowers(t *testing.T) {
	server, _ := definitionsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"etag-1"`)
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(cachedFlagDefinitions))
	})

	provider := &fakeFlagDefinitionCache{shouldFetch: false}
	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	_, _, _, published := provider.calls()
	require.Empty(t, published)
}

func TestFlagDefinitionCacheQuotaLimitedDoesNotPublish(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, requests := definitionsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	})

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests())
	_, _, _, published := provider.calls()
	require.Empty(t, published)
}

func TestFlagDefinitionCacheProviderNotCalledWithoutConfiguration(t *testing.T) {
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newTestPoller(t, server.URL)
	poller.fetchNewFeatureFlags()

	require.Equal(t, 1, requests())
	require.NotNil(t, poller.state.Load())
}

func TestFlagDefinitionCacheShutdownReleasesProvider(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, _ := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller, err := newFeatureFlagsPoller(
		"test-api-key",
		"test-personal-key",
		newDefaultLogger(false),
		server.URL,
		http.Client{},
		time.Hour,
		nil,
		10*time.Second,
		nil,
		false,
		provider,
	)
	require.NoError(t, err)

	<-poller.firstFeatureFlagRequestFinished
	poller.shutdownPoller(context.Background())

	_, _, shutdownCalls, _ := provider.calls()
	require.Equal(t, 1, shutdownCalls, "shutdownPoller waits for the provider to be released")

	select {
	case <-poller.pollLoopDone:
	default:
		t.Fatal("Shutdown ran while the polling loop was still running")
	}
}

func TestFlagDefinitionCacheShutdownErrorDoesNotBlockShutdown(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true, shutdownErr: errors.New("redis down")}
	server, _ := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.shutdown = make(chan bool)

	done := make(chan struct{})
	go func() {
		poller.shutdownPoller(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdownPoller did not return after a failing provider shutdown")
	}

	_, _, shutdownCalls, _ := provider.calls()
	require.Equal(t, 1, shutdownCalls)
}

func TestFlagDefinitionCacheShutdownUsesTheCallerDeadline(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool

	provider := &fakeFlagDefinitionCache{
		shouldFetch: true,
		onShutdown: func(ctx context.Context) error {
			deadline, hasDeadline = ctx.Deadline()
			return nil
		},
	}
	server, _ := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.shutdown = make(chan bool)

	callerDeadline := time.Now().Add(20 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()
	poller.shutdownPoller(ctx)

	require.True(t, hasDeadline, "Shutdown was handed a context with no deadline")
	require.Equal(t, callerDeadline, deadline, "the provider must not get its own, longer deadline")
}

func TestFlagDefinitionCacheShutdownStopsWaitingOnADeadDeadline(t *testing.T) {
	provider := &fakeFlagDefinitionCache{shouldFetch: true}
	server, _ := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	poller := newCachingTestPoller(t, server.URL, provider)
	poller.shutdown = make(chan bool)
	// A polling loop that never returns.
	poller.pollLoopDone = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		poller.shutdownPoller(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdownPoller kept waiting for the polling loop past the deadline")
	}

	_, _, shutdownCalls, _ := provider.calls()
	require.Equal(t, 1, shutdownCalls)
}

func TestFlagDefinitionCacheProviderThroughClient(t *testing.T) {
	var cached FlagDefinitionCacheData
	require.NoError(t, json.Unmarshal([]byte(cachedFlagDefinitions), &cached))

	provider := &fakeFlagDefinitionCache{shouldFetch: false, cached: &cached}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))

	client, err := NewWithConfig("phc_test", Config{
		Endpoint:                           server.URL,
		SecretKey:                          "phs_test",
		Interval:                           time.Millisecond,
		DefaultFeatureFlagsPollingInterval: time.Hour,
		FlagDefinitionCacheProvider:        provider,
	})
	require.NoError(t, err)

	value, err := client.GetFeatureFlag(FeatureFlagPayload{
		Key:                 "cached-flag",
		DistinctId:          "user-1",
		OnlyEvaluateLocally: true,
	})
	require.NoError(t, err)
	require.Equal(t, true, value)
	require.Zero(t, requests(), "definitions came from the cache, not the API")

	require.NoError(t, client.Close())

	_, getCalls, shutdownCalls, _ := provider.calls()
	require.GreaterOrEqual(t, getCalls, 1)
	require.Equal(t, 1, shutdownCalls, "closing the client releases the provider")
}
