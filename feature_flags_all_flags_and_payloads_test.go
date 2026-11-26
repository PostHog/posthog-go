package posthog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetAllFlagsAndPayloadsLocally(t *testing.T) {
	client := newFeatureFlagsLocalClient(t, "feature_flag/test-multivariate-flag.json")

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{
		DistinctId:          "distinct-id",
		OnlyEvaluateLocally: true,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"multivariate-flag": "fifth-variant"}, flags)
	require.Equal(t, map[string]string{"multivariate-flag": `{"test": 5}`}, payloads)
}

func TestGetAllFlagsAndPayloadsFromRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
			w.Write([]byte(fixture("test-flags-v3.json")))
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{Endpoint: server.URL})
	require.NoError(t, err)
	defer client.Close()

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{DistinctId: "distinct-id"})
	require.NoError(t, err)

	require.Equal(t, "decide-fallback-value", flags["beta-feature"])
	require.Equal(t, true, flags["enabled-flag"])
	require.Equal(t, false, flags["disabled-flag"])

	require.Equal(t, `{"foo": 1}`, payloads["enabled-flag"])
	require.Equal(t, `{"bar": 2}`, payloads["simple-flag"])
	require.Equal(t, `{"foo": "bar"}`, payloads["beta-feature"])
	// A flag the project configured no payload for stays out of the payload map,
	// rather than carrying an empty string.
	require.NotContains(t, payloads, "disabled-flag")
	require.NotContains(t, payloads, "group-flag")
}

func TestGetAllFlagsAndPayloadsOmitsFailedRemoteFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		w.Write([]byte(`{"flags":{
			"failed-flag":{"key":"failed-flag","enabled":false,"failed":true,"metadata":{"payload":"{\"copy\":\"placeholder\"}"}},
			"beta-ui":{"key":"beta-ui","enabled":true,"metadata":{"payload":"{\"color\":\"green\"}"}},
			"disabled-flag":{"key":"disabled-flag","enabled":false}
		}}`))
	}))
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
	require.NoError(t, err)
	defer client.Close()

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{
		DistinctId:            "user-123",
		SendFeatureFlagEvents: Ptr(false),
	})
	require.NoError(t, err)

	require.Equal(t, map[string]interface{}{"beta-ui": true, "disabled-flag": false}, flags)
	require.Equal(t, map[string]string{"beta-ui": `{"color":"green"}`}, payloads)
}

func TestGetAllFlagsAndPayloadsFallsBackToRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		if r.URL.Path == "/flags" || r.URL.Path == "/flags/" {
			w.Write([]byte(fixture("test-flags-v3.json")))
		} else if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		SecretKey: "some very secret key",
		Endpoint:  server.URL,
	})
	require.NoError(t, err)
	defer client.Close()

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{DistinctId: "distinct-id"})
	require.NoError(t, err)
	require.Equal(t, "decide-fallback-value", flags["beta-feature"])
	require.Equal(t, `{"foo": "bar"}`, payloads["beta-feature"])
}

func TestGetAllFlagsAndPayloadsWithoutSecretKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		w.Write([]byte(fixture("test-flags-v3.json")))
	}))
	defer server.Close()

	client, err := NewWithConfig("test-api-key", Config{Endpoint: server.URL})
	require.NoError(t, err)
	defer client.Close()

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{
		DistinctId:          "distinct-id",
		OnlyEvaluateLocally: true,
	})
	require.ErrorIs(t, err, ErrNoSecretKey)
	require.Nil(t, flags)
	require.Nil(t, payloads)
}

func TestGetAllFlagsAndPayloadsKeepsLocalPairWhenRemoteFails(t *testing.T) {
	definitions := `{"flags":[
		{"id":1,"name":"Beta UI","key":"beta-ui","active":true,"filters":{
			"groups":[{"properties":[],"rollout_percentage":100}],
			"payloads":{"true":"{\"copy\":\"local\"}"}
		}},
		{"id":2,"name":"Checkout","key":"checkout","active":true,"filters":{
			"groups":[{"properties":[{"key":"email","operator":"exact","value":"x@example.com","type":"person"}],"rollout_percentage":100}]
		}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCaptureOK(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/flags/definitions") {
			w.Write([]byte(definitions))
			return
		}
		w.Write([]byte(`{"flags":{
			"beta-ui":{"key":"beta-ui","enabled":false,"failed":true,"metadata":{"payload":"{\"copy\":\"remote\"}"}},
			"checkout":{"key":"checkout","enabled":false,"failed":true,"metadata":{"payload":"{\"copy\":\"remote\"}"}}
		}}`))
	}))
	defer server.Close()

	client, err := NewWithConfig("Csyjlnlun3OzyNJAafdlv", Config{
		SecretKey: "some very secret key",
		Endpoint:  server.URL,
	})
	require.NoError(t, err)
	defer client.Close()

	flags, payloads, err := client.GetAllFlagsAndPayloads(FeatureFlagPayloadNoKey{DistinctId: "distinct-id"})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"beta-ui": true}, flags)
	require.Equal(t, map[string]string{"beta-ui": `{"copy":"local"}`}, payloads)
}
