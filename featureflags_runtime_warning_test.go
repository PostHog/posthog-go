package posthog

import (
	"fmt"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

var warningRuntimes = []struct {
	name  string
	field string
}{
	{name: "client", field: `,"evaluation_runtime":"client"`},
	{name: "server", field: `,"evaluation_runtime":"server"`},
	{name: "all", field: `,"evaluation_runtime":"all"`},
	{name: "null", field: `,"evaluation_runtime":null`},
	{name: "absent"},
}

func runtimeWarningDefinition(key string, reason UnresolvedFlagReason, runtimeField string) string {
	definition := flagDefinition(key, flagCondition(100))
	if reason == UnresolvedFlagReasonExperienceContinuity {
		definition = strings.TrimSuffix(definition, "}") + `,"ensure_experience_continuity":true}`
	} else if reason == UnresolvedFlagReasonUnsupportedDefinition {
		definition = flagDefinition(key, flagCondition(100, staticCohortFilter))
	}
	return strings.TrimSuffix(definition, "}") + `,"version":17` + runtimeField + "}"
}

func reserializeWarningDefinitions(t *testing.T, definitions string) string {
	t.Helper()
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(definitions), &decoded))
	encoded, err := json.MarshalIndent(decoded, "", "  ")
	require.NoError(t, err)
	require.NotEqual(t, definitions, string(encoded))
	return string(encoded)
}

func TestUnresolvableFlagWarningRuntimeJSONLoads(t *testing.T) {
	reasons := []UnresolvedFlagReason{UnresolvedFlagReasonExperienceContinuity, UnresolvedFlagReasonUnsupportedDefinition}
	var flags []string
	expectedUnresolved := map[string]UnresolvedFlagReason{}
	for _, reason := range reasons {
		for _, runtime := range warningRuntimes {
			key := string(reason) + "-" + runtime.name
			flags = append(flags, runtimeWarningDefinition(key, reason, runtime.field))
			expectedUnresolved[key] = reason
		}
	}
	flags = append(flags, runtimeWarningDefinition("resolvable-client", "", warningRuntimes[0].field))
	definitions := localDefinitions(flags...)
	reserialized := reserializeWarningDefinitions(t, definitions)

	for _, source := range []string{"API", "cache"} {
		t.Run(source, func(t *testing.T) {
			server, remoteRequests := definitionsSequenceServer(t, definitions, definitions, "304")
			provider := &fakeFlagDefinitionCache{shouldFetch: source == "API", cached: json.RawMessage(definitions)}
			poller := newCachingTestPoller(t, server.URL, provider)
			logger := &captureLogger{}
			poller.Logger = logger
			poller.fetchNewFeatureFlags()

			warnings := logger.snapshot()
			require.Len(t, warnings, 2)
			for i, reason := range reasons {
				t.Run(string(reason), func(t *testing.T) {
					require.NotContains(t, warnings[i], string(reason)+"-client", "client-only warning leaked from JSON definitions")
					var expectedKeys []string
					for _, runtime := range warningRuntimes {
						key := string(reason) + "-" + runtime.name
						if runtime.name == "client" {
							require.NotContains(t, poller.warnedUnresolvableFlags, key)
						} else {
							expectedKeys = append(expectedKeys, key)
							require.Equal(t, 1, countUnresolvableWarnings(logger, key, reason))
						}
					}
					require.Contains(t, warnings[i], fmt.Sprintf("|4|%q|%q", reason, strings.Join(expectedKeys, ", ")))
				})
			}
			require.Len(t, poller.warnedUnresolvableFlags, 8)
			state := poller.state.Load()
			require.Len(t, state.featureFlags, len(flags))
			require.Len(t, state.flagsByKey, len(flags))
			require.Equal(t, definitions, string(state.definitions))
			// Check the typed JSON view as well as the separately retained raw snapshot.
			for _, reason := range reasons {
				encoded, err := json.Marshal(state.flagsByKey[string(reason)+"-client"])
				require.NoError(t, err)
				require.Contains(t, string(encoded), `"evaluation_runtime":"client"`)
			}
			require.Zero(t, remoteRequests.Load(), "definition loading must not evaluate flags")

			c := &client{Config: Config{Logger: silentLogger{}}, featureFlagsPoller: poller}
			snapshot, err := c.EvaluateFlags(EvaluateFlagsPayload{DistinctId: "user-123", OnlyEvaluateLocally: true})
			require.NoError(t, err)
			require.Equal(t, []string{"resolvable-client"}, snapshot.Keys())
			require.True(t, snapshot.flags["resolvable-client"].Enabled)
			require.Equal(t, expectedUnresolved, snapshot.UnresolvedFlags())

			// API repeats include an unchanged 200 and a 304 before cache hydration.
			if source == "API" {
				poller.fetchNewFeatureFlags()
				poller.fetchNewFeatureFlags()
				_, _, _, published := provider.calls()
				require.Len(t, published, 3)
				for _, raw := range published {
					require.Equal(t, definitions, string(raw))
				}
			}
			provider.mu.Lock()
			provider.shouldFetch = false
			provider.cached = json.RawMessage(reserialized)
			provider.mu.Unlock()
			poller.fetchNewFeatureFlags()
			poller.fetchNewFeatureFlags()
			require.Equal(t, reserialized, string(poller.state.Load().definitions))
			require.Equal(t, warnings, logger.snapshot(), "unchanged/reordered definitions must not rewarn")
			_, cacheLoads, _, published := provider.calls()
			if source == "cache" {
				require.Equal(t, 3, cacheLoads)
				require.Empty(t, published, "cache-only loads must not publish API definitions")
			} else {
				require.Equal(t, 2, cacheLoads)
			}
			require.Zero(t, remoteRequests.Load())
		})
	}
}

func TestUnresolvableFlagWarningRuntimeHistoryJSONTransitions(t *testing.T) {
	for _, reason := range []UnresolvedFlagReason{UnresolvedFlagReasonExperienceContinuity, UnresolvedFlagReasonUnsupportedDefinition} {
		for _, runtime := range warningRuntimes[1:] {
			for _, initialSource := range []string{"API", "cache"} {
				for _, refreshSource := range []string{"API", "cache"} {
					name := fmt.Sprintf("%s/%s/%s-to-%s", reason, runtime.name, initialSource, refreshSource)
					t.Run(name, func(t *testing.T) {
						original := localDefinitions(runtimeWarningDefinition("checkout", reason, runtime.field))
						clientOnly := localDefinitions(runtimeWarningDefinition("checkout", reason, warningRuntimes[0].field))
						var apiBodies []string
						if initialSource == "API" {
							apiBodies = append(apiBodies, original)
						}
						if refreshSource == "API" {
							apiBodies = append(apiBodies, clientOnly, original, original)
						}
						if len(apiBodies) == 0 {
							apiBodies = []string{"402"}
						}
						server, remoteRequests := definitionsSequenceServer(t, apiBodies...)
						provider := &fakeFlagDefinitionCache{}
						poller := newCachingTestPoller(t, server.URL, provider)
						logger := &captureLogger{}
						poller.Logger = logger
						load := func(source, definitions string) {
							provider.mu.Lock()
							provider.shouldFetch = source == "API"
							provider.cached = json.RawMessage(definitions)
							provider.mu.Unlock()
							poller.fetchNewFeatureFlags()
							require.Equal(t, definitions, string(poller.state.Load().definitions))
						}

						load(initialSource, original)
						require.Equal(t, 1, countUnresolvableWarnings(logger, "checkout", reason))
						originalFingerprint := poller.warnedUnresolvableFlags["checkout"]
						load(refreshSource, clientOnly)
						require.Len(t, logger.snapshot(), 1, "becoming client must not warn")
						require.Empty(t, poller.warnedUnresolvableFlags, "becoming client must forget warning history")
						require.Contains(t, poller.state.Load().flagsByKey, "checkout")
						load(refreshSource, original)
						require.Equal(t, originalFingerprint, poller.warnedUnresolvableFlags["checkout"], "restore the identical original definition/version")
						require.Equal(t, 2, countUnresolvableWarnings(logger, "checkout", reason))
						load(refreshSource, original)
						require.Len(t, logger.snapshot(), 2)
						require.Zero(t, remoteRequests.Load())
					})
				}
			}
		}
	}
}
