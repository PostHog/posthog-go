package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newFlagDependencyClient(t *testing.T, definitions string, remoteFallback bool) Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(definitions))
		} else if remoteFallback && strings.HasPrefix(r.URL.Path, "/flags/") {
			w.Write([]byte(`{"featureFlags": {}}`))
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewWithConfig("test-api-key", Config{PersonalApiKey: "test-personal-api-key", Endpoint: server.URL})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func assertDependencyFlag(t *testing.T, client Client, key string, want bool) {
	t.Helper()
	result, err := client.IsFeatureEnabled(FeatureFlagPayload{Key: key, DistinctId: "test-user"})
	require.NoError(t, err)
	require.Equal(t, want, result)
}

const missingFlagDependencyDefinitions = `{
	"flags": [{"id": 1, "name": "Flag A", "key": "flag-a", "active": true, "filters": {"groups": [{"properties": [{"key": "non-existent-flag", "operator": "flag_evaluates_to", "value": true, "type": "flag", "dependency_chain": ["non-existent-flag"]}], "rollout_percentage": 100}]}}],
	"group_type_mapping": {}
}`

const mixedConditionsDependencyDefinitions = `{
	"flags": [
		{"id": 1, "name": "Base Flag", "key": "base-flag", "active": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}},
		{"id": 2, "name": "Mixed Flag", "key": "mixed-flag", "active": true, "filters": {"groups": [{"properties": [{"key": "base-flag", "operator": "flag_evaluates_to", "value": true, "type": "flag", "dependency_chain": ["base-flag"]}, {"key": "email", "operator": "icontains", "value": "@example.com", "type": "person"}], "rollout_percentage": 100}]}}
	],
	"group_type_mapping": {}
}`

func assertLocalDependencyFlag(t *testing.T, client Client, key, distinctID, email string, want bool) {
	t.Helper()
	result, err := client.IsFeatureEnabled(FeatureFlagPayload{
		Key:                 key,
		DistinctId:          distinctID,
		PersonProperties:    NewProperties().Set("email", email),
		OnlyEvaluateLocally: true,
	})
	require.NoError(t, err)
	require.Equal(t, want, result)
}

const malformedFlagDependencyDefinitions = `{
	"flags": [
		{"id": 1, "name": "Base Flag", "key": "base-flag", "active": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}]}},
		{"id": 2, "name": "Missing Chain Flag", "key": "missing-chain-flag", "active": true, "filters": {"groups": [{"properties": [{"key": "base-flag", "operator": "flag_evaluates_to", "value": true, "type": "flag"}], "rollout_percentage": 100}]}}
	],
	"group_type_mapping": {}
}`

func TestFlagDependenciesSimpleChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"name": "Flag A",
						"key": "flag-a",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "email",
											"operator": "icontains",
											"value": "@example.com",
											"type": "person"
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					},
					{
						"id": 2,
						"name": "Flag B",
						"key": "flag-b",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "flag-a",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": ["flag-a"]
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					}
				],
				"group_type_mapping": {}
			}`))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Test when dependency is satisfied
	result, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-b",
			DistinctId:          "test-user",
			PersonProperties:    NewProperties().Set("email", "test@example.com"),
			OnlyEvaluateLocally: true,
		},
	)
	require.NoError(t, err)
	require.Equal(t, true, result)

	// Test flag-a directly with email that doesn't match - should be false
	flagAResult, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-a",
			DistinctId:          "test-user-2",
			PersonProperties:    NewProperties().Set("email", "test@other.com"),
			OnlyEvaluateLocally: true,
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, flagAResult, "flag-a should be false for email that doesn't contain @example.com")

	// Test when dependency is not satisfied
	result, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-b",
			DistinctId:          "test-user-2",
			PersonProperties:    NewProperties().Set("email", "test@other.com"),
			OnlyEvaluateLocally: true,
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, result)
}

func TestFlagDependenciesCircularDependency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"name": "Flag A",
						"key": "flag-a",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "flag-b",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": []
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					},
					{
						"id": 2,
						"name": "Flag B",
						"key": "flag-b",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "flag-a",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": []
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					}
				],
				"group_type_mapping": {}
			}`))
		} else if strings.HasPrefix(r.URL.Path, "/flags/") {
			w.Write([]byte(`{"featureFlags": {}}`))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Both flags should fall back to remote evaluation due to circular dependency
	result, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "flag-a",
			DistinctId: "test-user",
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, result)

	result, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "flag-b",
			DistinctId: "test-user",
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, result)
}

func TestFlagDependenciesMissingFlag(t *testing.T) {
	assertDependencyFlag(t, newFlagDependencyClient(t, missingFlagDependencyDefinitions, true), "flag-a", false)
}

func TestFlagDependenciesComplexChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"name": "Flag A",
						"key": "flag-a",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [],
									"rollout_percentage": 100
								}
							]
						}
					},
					{
						"id": 2,
						"name": "Flag B",
						"key": "flag-b",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [],
									"rollout_percentage": 100
								}
							]
						}
					},
					{
						"id": 3,
						"name": "Flag C",
						"key": "flag-c",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "flag-a",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": ["flag-a"]
										},
										{
											"key": "flag-b",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": ["flag-b"]
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					},
					{
						"id": 4,
						"name": "Flag D",
						"key": "flag-d",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "flag-c",
											"operator": "flag_evaluates_to",
											"value": true,
											"type": "flag",
											"dependency_chain": ["flag-a", "flag-b", "flag-c"]
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					}
				],
				"group_type_mapping": {}
			}`))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// All dependencies satisfied - should return true
	result, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:                 "flag-d",
			DistinctId:          "test-user",
			OnlyEvaluateLocally: true,
		},
	)
	require.NoError(t, err)
	require.Equal(t, true, result)
}

func TestFlagDependenciesMixedConditions(t *testing.T) {
	client := newFlagDependencyClient(t, mixedConditionsDependencyDefinitions, false)

	// Both flag dependency and email condition satisfied
	assertLocalDependencyFlag(t, client, "mixed-flag", "test-user", "test@example.com", true)

	// Flag dependency satisfied but email condition not satisfied
	assertLocalDependencyFlag(t, client, "mixed-flag", "test-user-2", "test@other.com", false)
}

func TestFlagDependenciesMalformedChain(t *testing.T) {
	assertDependencyFlag(t, newFlagDependencyClient(t, malformedFlagDependencyDefinitions, true), "missing-chain-flag", false)
}

func TestMultiLevelMultivariateDependencyChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(`{
				"flags": [
					{
						"id": 1,
						"name": "Leaf Flag",
						"key": "leaf-flag",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "email",
											"operator": "icontains",
											"value": "control",
											"type": "person"
										}
									],
									"rollout_percentage": 100,
									"variant": "control"
								},
								{
									"properties": [
										{
											"key": "email",
											"operator": "icontains",
											"value": "test",
											"type": "person"
										}
									],
									"rollout_percentage": 100,
									"variant": "test"
								}
							],
							"multivariate": {
								"variants": [
									{"key": "control", "name": "Control", "rollout_percentage": 50},
									{"key": "test", "name": "Test", "rollout_percentage": 50}
								]
							}
						}
					},
					{
						"id": 2,
						"name": "Intermediate Flag",
						"key": "intermediate-flag",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "leaf-flag",
											"operator": "flag_evaluates_to",
											"value": "control",
											"type": "flag",
											"dependency_chain": ["leaf-flag"]
										}
									],
									"rollout_percentage": 100,
									"variant": "blue"
								}
							],
							"multivariate": {
								"variants": [
									{"key": "blue", "name": "Blue", "rollout_percentage": 60},
									{"key": "green", "name": "Green", "rollout_percentage": 40}
								]
							}
						}
					},
					{
						"id": 3,
						"name": "Dependent Flag",
						"key": "dependent-flag",
						"active": true,
						"filters": {
							"groups": [
								{
									"properties": [
										{
											"key": "intermediate-flag",
											"operator": "flag_evaluates_to",
											"value": "blue",
											"type": "flag",
											"dependency_chain": ["leaf-flag", "intermediate-flag"]
										}
									],
									"rollout_percentage": 100
								}
							]
						}
					}
				],
				"group_type_mapping": {}
			}`))
		} else if strings.HasPrefix(r.URL.Path, "/flags/") {
			w.Write([]byte(`{"featureFlags": {}}`))
		}
	}))
	defer server.Close()

	client, _ := NewWithConfig("test-api-key", Config{
		PersonalApiKey: "test-personal-api-key",
		Endpoint:       server.URL,
	})
	defer client.Close()

	// Test leaf flag evaluates to "control" variant for control@example.com
	result, err := client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "leaf-flag",
			DistinctId: "user-control",
			PersonProperties: NewProperties().
				Set("email", "control@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, "control", result)

	// Test leaf flag evaluates to "test" variant for test@example.com
	result, err = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "leaf-flag",
			DistinctId: "user-test",
			PersonProperties: NewProperties().
				Set("email", "test@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, "test", result)

	// Test intermediate flag evaluates to "blue" when leaf-flag is "control"
	result, err = client.GetFeatureFlag(
		FeatureFlagPayload{
			Key:        "intermediate-flag",
			DistinctId: "user-control",
			PersonProperties: NewProperties().
				Set("email", "control@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, "blue", result)

	// Test intermediate flag evaluates to false when leaf-flag is "test" (dependency not met)
	resultBool, err := client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "intermediate-flag",
			DistinctId: "user-test",
			PersonProperties: NewProperties().
				Set("email", "test@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, resultBool)

	// Test dependent flag is true when leaf-flag="control" and intermediate-flag="blue"
	resultBool, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "dependent-flag",
			DistinctId: "user-control",
			PersonProperties: NewProperties().
				Set("email", "control@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, true, resultBool)

	// Test dependent flag is false when leaf-flag="test" (breaks dependency chain)
	resultBool, err = client.IsFeatureEnabled(
		FeatureFlagPayload{
			Key:        "dependent-flag",
			DistinctId: "user-test",
			PersonProperties: NewProperties().
				Set("email", "test@example.com"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, false, resultBool)
}
