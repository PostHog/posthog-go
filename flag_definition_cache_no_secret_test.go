package posthog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newProviderOnlyTestClient(t *testing.T, provider FlagDefinitionCacheProvider, config Config) (Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags") {
			requests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	config.Endpoint = server.URL
	config.DefaultFeatureFlagsPollingInterval = time.Hour
	config.FlagDefinitionCacheProvider = provider
	cli, err := NewWithConfig("phc_test", config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli, &requests
}

func TestFlagDefinitionCacheWithoutSecretKeyPublicEvaluation(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("localOnly=%t", localOnly), func(t *testing.T) {
			provider := &fakeFlagDefinitionCache{shouldFetch: true, cached: json.RawMessage(cachedFlagDefinitions)}
			cli, requests := newProviderOnlyTestClient(t, provider, Config{SecretKey: " \t", PersonalApiKey: "\n "})
			result, err := cli.GetFeatureFlagResult(FeatureFlagPayload{
				Key:                   "cached-flag",
				DistinctId:            "user-1",
				OnlyEvaluateLocally:   localOnly,
				SendFeatureFlagEvents: Ptr(false),
			})
			require.NoError(t, err)
			require.True(t, result.Enabled)
			require.JSONEq(t, `{"from":"cache"}`, *result.RawPayload)

			flags, err := cli.GetAllFlags(FeatureFlagPayloadNoKey{
				DistinctId:          "user-1",
				PersonProperties:    Properties{"plan": "enterprise"},
				OnlyEvaluateLocally: localOnly,
			})
			require.NoError(t, err)
			require.Equal(t, map[string]interface{}{"cached-flag": true, "cached-multivariate": "control"}, flags)

			snapshot, err := cli.EvaluateFlags(EvaluateFlagsPayload{
				DistinctId:          "user-1",
				PersonProperties:    Properties{"plan": "enterprise"},
				OnlyEvaluateLocally: localOnly,
			})
			require.NoError(t, err)
			require.Equal(t, "control", snapshot.GetFlag("cached-multivariate"))
			require.JSONEq(t, `{"from":"cache"}`, snapshot.GetFlagPayload("cached-flag"))
			require.NoError(t, cli.Close())
			require.Zero(t, requests.Load(), "neither definitions nor remote evaluation should be requested")
			shouldFetch, get, shutdown, published := provider.calls()
			require.Zero(t, shouldFetch)
			require.Equal(t, 1, get)
			require.Equal(t, 1, shutdown)
			require.Empty(t, published)
		})
	}
}

func TestFlagDefinitionCacheWithoutSecretKeyRefresh(t *testing.T) {
	for _, mode := range []string{"reload", "poll"} {
		t.Run(mode, func(t *testing.T) {
			provider := &fakeFlagDefinitionCache{cached: json.RawMessage(cachedFlagDefinitions)}
			config := Config{}
			if mode == "poll" {
				config.NextFeatureFlagsPollingTick = func() time.Duration { return 10 * time.Millisecond }
			}
			cli, requests := newProviderOnlyTestClient(t, provider, config)
			payload := FeatureFlagPayload{Key: "cached-flag", DistinctId: "user-1", OnlyEvaluateLocally: true, SendFeatureFlagEvents: Ptr(false)}
			value, err := cli.GetFeatureFlag(payload)
			require.NoError(t, err)
			require.Equal(t, true, value)

			provider.mu.Lock()
			provider.cached = json.RawMessage(strings.Replace(cachedFlagDefinitions, `"active": true`, `"active": false`, 1))
			provider.mu.Unlock()
			if mode == "reload" {
				require.NoError(t, cli.ReloadFeatureFlags())
			}
			require.Eventually(t, func() bool {
				value, err := cli.GetFeatureFlag(payload)
				return err == nil && value == false
			}, time.Second, time.Millisecond)
			require.NoError(t, cli.Close())
			require.Zero(t, requests.Load())
			shouldFetch, get, shutdown, published := provider.calls()
			require.Zero(t, shouldFetch)
			require.GreaterOrEqual(t, get, 2)
			require.Equal(t, 1, shutdown)
			require.Empty(t, published)
		})
	}
}

func TestFlagDefinitionCacheWithoutSecretKeyFetchBoundary(t *testing.T) {
	for _, test := range []struct {
		name           string
		shouldFetch    bool
		shouldFetchErr error
		cached         json.RawMessage
		getErr         error
	}{
		{name: "fetch elected", shouldFetch: true, cached: json.RawMessage(cachedFlagDefinitions)},
		{name: "decision failure", shouldFetchErr: errors.New("decision failed"), cached: json.RawMessage(cachedFlagDefinitions)},
		{name: "cache miss"},
		{name: "cache failure", getErr: errors.New("cache failed")},
		{name: "invalid cache", cached: json.RawMessage(`{}`)},
		{name: "malformed cache", cached: json.RawMessage(`{`)},
	} {
		for _, warm := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/warm=%t", test.name, warm), func(t *testing.T) {
				provider := &fakeFlagDefinitionCache{}
				if warm {
					provider.cached = json.RawMessage(fmt.Sprintf(matchingVersionDefinitions, `,"property_matching_version":2`))
				}
				refreshed := make(chan struct{}, 2)
				cli, requests := newProviderOnlyTestClient(t, provider, Config{
					NextFeatureFlagsPollingTick: func() time.Duration {
						refreshed <- struct{}{}
						return time.Hour
					},
				})
				payload := FeatureFlagPayload{Key: "person", DistinctId: "user-1", PersonProperties: Properties{"value": "banana"}, OnlyEvaluateLocally: true, SendFeatureFlagEvents: Ptr(false)}
				if warm {
					result, err := cli.GetFeatureFlagResult(payload)
					require.NoError(t, err)
					require.False(t, result.Enabled, "v2 metadata must be loaded with the snapshot")
				} else {
					// Wait for the initial empty-cache load to finish through a public evaluation.
					_, _ = cli.GetFeatureFlagResult(payload)
				}
				select {
				case <-refreshed:
				case <-time.After(time.Second):
					t.Fatal("initial cache refresh did not finish")
				}
				provider.mu.Lock()
				provider.shouldFetch = test.shouldFetch
				provider.shouldFetchErr = test.shouldFetchErr
				provider.cached = test.cached
				provider.getErr = test.getErr
				provider.mu.Unlock()
				_, getsBefore, _, _ := provider.calls()
				require.NoError(t, cli.ReloadFeatureFlags())
				select {
				case <-refreshed:
				case <-time.After(time.Second):
					t.Fatal("manual cache refresh did not finish")
				}
				if test.shouldFetch || test.shouldFetchErr != nil {
					value, err := cli.GetFeatureFlag(FeatureFlagPayload{Key: "cached-flag", DistinctId: "user-1", OnlyEvaluateLocally: true, SendFeatureFlagEvents: Ptr(false)})
					require.NoError(t, err)
					require.Equal(t, true, value, "cache is read regardless of the fetch decision")
				} else if warm {
					result, err := cli.GetFeatureFlagResult(payload)
					require.NoError(t, err)
					require.False(t, result.Enabled, "failed refresh preserves definitions and their v2 selector")
				}
				require.NoError(t, cli.Close())
				shouldFetch, get, shutdown, published := provider.calls()
				require.Zero(t, shouldFetch)
				require.Equal(t, getsBefore+1, get)
				require.Equal(t, 1, shutdown)
				require.Empty(t, published)
				require.Zero(t, requests.Load(), "missing auth must stop before an HTTP request")
			})
		}
	}
}

// leaseFlagDefinitionCache models a shared fetch lease that remains claimed until
// the provider releases it, independently of whether a fetch actually occurred.
type leaseFlagDefinitionCache struct {
	fakeFlagDefinitionCache
	leaseClaimed bool
}

func (c *leaseFlagDefinitionCache) ShouldFetchFlagDefinitions(context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shouldFetchCalls++
	if c.leaseClaimed {
		return false, nil
	}
	c.leaseClaimed = true
	return true, nil
}

func (c *leaseFlagDefinitionCache) OnFlagDefinitionsReceived(ctx context.Context, data json.RawMessage) error {
	if err := c.fakeFlagDefinitionCache.OnFlagDefinitionsReceived(ctx, data); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = data
	return nil
}

func TestFlagDefinitionCacheWithoutSecretKeyDoesNotClaimSharedLease(t *testing.T) {
	provider := &leaseFlagDefinitionCache{
		fakeFlagDefinitionCache: fakeFlagDefinitionCache{cached: json.RawMessage(cachedFlagDefinitions)},
		leaseClaimed:            true,
	}
	updated := strings.Replace(cachedFlagDefinitions, `"active": true`, `"active": false`, 1)
	server, requests := definitionsServer(t, serveDefinitions(updated))
	publisher := newCachingTestPoller(t, server.URL, provider)
	publisher.fetchNewFeatureFlags()
	require.True(t, publisher.state.Load().flagsByKey["cached-flag"].Active)
	require.Zero(t, requests())

	// The lease is now available for the next refresh cycle. A cache reader must
	// not claim it before the credentialed publisher has a chance to refresh.
	provider.leaseClaimed = false
	reader := newCachingTestPoller(t, server.URL, provider)
	reader.personalApiKey = ""
	reader.fetchNewFeatureFlags()
	require.False(t, provider.leaseClaimed)
	require.True(t, reader.state.Load().flagsByKey["cached-flag"].Active)
	shouldFetch, get, _, published := provider.calls()
	require.Equal(t, 1, shouldFetch, "only the initial keyed refresh consults the lease")
	require.Equal(t, 2, get)
	require.Empty(t, published)
	require.Zero(t, requests())

	publisher.fetchNewFeatureFlags()
	require.False(t, publisher.state.Load().flagsByKey["cached-flag"].Active)
	require.Equal(t, 1, requests())
	reader.fetchNewFeatureFlags()
	require.False(t, reader.state.Load().flagsByKey["cached-flag"].Active)
	shouldFetch, get, _, published = provider.calls()
	require.Equal(t, 2, shouldFetch, "only keyed refreshes consult the lease")
	require.Equal(t, 3, get)
	require.Len(t, published, 1)
	require.JSONEq(t, updated, string(published[0]))
}

func TestFlagDefinitionCacheWithoutSecretKeySkipsPanickingDecision(t *testing.T) {
	provider := &fakeFlagDefinitionCache{
		cached: json.RawMessage(cachedFlagDefinitions),
		onShouldFetch: func(context.Context) {
			panic("cache-only readers must not acquire fetch leadership")
		},
	}
	cli, requests := newProviderOnlyTestClient(t, provider, Config{})
	value, err := cli.GetFeatureFlag(FeatureFlagPayload{
		Key: "cached-flag", DistinctId: "user-1", OnlyEvaluateLocally: true, SendFeatureFlagEvents: Ptr(false),
	})
	require.NoError(t, err)
	require.Equal(t, true, value)
	require.NoError(t, cli.Close())
	shouldFetch, get, shutdown, published := provider.calls()
	require.Zero(t, shouldFetch)
	require.Equal(t, 1, get)
	require.Equal(t, 1, shutdown)
	require.Empty(t, published)
	require.Zero(t, requests.Load())
}

func TestFlagDefinitionCacheWithoutSecretKeyDirectFetchGuard(t *testing.T) {
	provider := &fakeFlagDefinitionCache{cached: json.RawMessage(cachedFlagDefinitions)}
	server, requests := definitionsServer(t, serveDefinitions(cachedFlagDefinitions))
	poller := newCachingTestPoller(t, server.URL, provider)
	poller.personalApiKey = ""
	poller.fetchNewFeatureFlags()
	previous := poller.state.Load()
	require.NotNil(t, previous)

	poller.fetchFlagDefinitions(true)
	require.Same(t, previous, poller.state.Load())
	require.Zero(t, requests())
	shouldFetch, get, _, published := provider.calls()
	require.Zero(t, shouldFetch)
	require.Equal(t, 1, get)
	require.Empty(t, published)
}
