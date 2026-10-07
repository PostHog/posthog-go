package posthog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptureMissingEvent(t *testing.T) {
	assertFieldError(t, Capture{DistinctId: "1"}, fieldError("posthog.Capture", "Event"))
}

func TestCaptureMissingDistinctId(t *testing.T) {
	assertFieldError(t, Capture{Event: "1"}, fieldError("posthog.Capture", "DistinctId"))
}

func TestCaptureValidWithDistinctId(t *testing.T) {
	assertValid(t, Capture{Event: "1", DistinctId: "2"})
}

func TestCaptureAPIfyIncludesIsServerProperty(t *testing.T) {
	assertIsServerProperty(t, Capture{Event: "test-event", DistinctId: "user-123", IsServer: true}.APIfy(), true)
}

func TestCaptureAPIfyOmitsIsServerWhenFalse(t *testing.T) {
	assertIsServerProperty(t, Capture{Event: "test-event", DistinctId: "user-123", IsServer: false}.APIfy(), false)
}

func TestCaptureAPIfyUsesCanonicalLibraryProperties(t *testing.T) {
	apiMsg, ok := Capture{Event: "test-event", DistinctId: "user-123"}.APIfy().(CaptureInApi)
	if !ok {
		t.Fatalf("expected CaptureInApi, got %T", apiMsg)
	}

	for _, tt := range []struct {
		key  string
		want interface{}
	}{
		{key: "$lib", want: SDKName},
		{key: "$lib_version", want: getVersion()},
	} {
		t.Run(tt.key, func(t *testing.T) {
			if got := apiMsg.Properties[tt.key]; got != tt.want {
				t.Errorf("%s: got %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestCaptureAPIfyOmitsIgnoredTopLevelFieldsFromJSON(t *testing.T) {
	apiMsg := Capture{
		Type:             "capture",
		Event:            "test-event",
		DistinctId:       "user-123",
		SendFeatureFlags: SendFeatureFlags(true),
	}.APIfy()

	data, err := json.Marshal(apiMsg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var wire map[string]interface{}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, key := range []string{"type", "library", "library_version", "send_feature_flags"} {
		if _, ok := wire[key]; ok {
			t.Errorf("%s should not be serialized on capture events", key)
		}
	}

	props, ok := wire["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}
	if got := props["$lib"]; got != SDKName {
		t.Errorf("$lib: got %v, want %s", got, SDKName)
	}
	if got := props["$lib_version"]; got != getVersion() {
		t.Errorf("$lib_version: got %v, want %s", got, getVersion())
	}
}

func TestAPIfyLibrary(t *testing.T) {
	exception := func(library string, properties Properties) Exception {
		return Exception{
			DistinctId:    "user-123",
			Library:       library,
			Properties:    properties,
			ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}},
		}
	}
	callerLib := Properties{"$lib": "caller-lib"}

	for _, tt := range []struct {
		name    string
		msg     Message
		wantLib string
	}{
		{name: "capture without Library", msg: Capture{Event: "e", DistinctId: "d"}, wantLib: "posthog-go"},
		{name: "capture with Library", msg: Capture{Event: "e", DistinctId: "d", Library: "posthog-go-mcp"}, wantLib: "posthog-go-mcp"},
		{name: "capture ignores caller $lib", msg: Capture{Event: "e", DistinctId: "d", Properties: callerLib}, wantLib: "posthog-go"},
		{name: "capture Library beats caller $lib", msg: Capture{Event: "e", DistinctId: "d", Library: "posthog-go-mcp", Properties: callerLib}, wantLib: "posthog-go-mcp"},
		{name: "exception without Library", msg: exception("", nil), wantLib: "posthog-go"},
		{name: "exception with Library", msg: exception("posthog-go-mcp", nil), wantLib: "posthog-go-mcp"},
		{name: "exception ignores caller $lib", msg: exception("", callerLib), wantLib: "posthog-go"},
		{name: "exception Library beats caller $lib", msg: exception("posthog-go-mcp", callerLib), wantLib: "posthog-go-mcp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg.APIfy())
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			var wire struct {
				Properties map[string]interface{} `json:"properties"`
			}
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if got := wire.Properties["$lib"]; got != tt.wantLib {
				t.Errorf("$lib: got %v, want %s", got, tt.wantLib)
			}
			if got := wire.Properties["$lib_version"]; got != "1.0.0" {
				t.Errorf("$lib_version: got %v, want 1.0.0", got)
			}
		})
	}
}

func TestExceptionAPIfyTopLevelLibrary(t *testing.T) {
	for library, want := range map[string]string{"": "posthog-go", "posthog-go-mcp": "posthog-go-mcp"} {
		msg := Exception{DistinctId: "d", Library: library, ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}}
		if got := msg.APIfy().(ExceptionInApi).Library; got != want {
			t.Errorf("Library %q: top-level library got %q, want %q", library, got, want)
		}
	}
}

func TestBeforeSendSeesEachMessageLibraryButTheBodyOmitsIt(t *testing.T) {
	body := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var env struct {
			Batch []struct {
				Uuid string `json:"uuid"`
			} `json:"batch"`
		}
		require.NoError(t, json.Unmarshal(payload, &env))
		results := map[string]eventResult{}
		for _, ev := range env.Batch {
			results[ev.Uuid] = eventResult{Result: resultOk}
		}
		body <- payload
		_, _ = w.Write([]byte(resultsBody(t, results)))
	}))
	defer server.Close()

	var seenByHook []string
	client, err := NewWithConfig("test-api-key", Config{
		Endpoint:  server.URL,
		BatchSize: 10,
		Logger:    toLogger(t),
		BeforeSend: func(msg Message) Message {
			switch m := msg.(type) {
			case Capture:
				seenByHook = append(seenByHook, m.Library)
			case Exception:
				seenByHook = append(seenByHook, m.Library)
			}
			return msg
		},
	})
	require.NoError(t, err)

	item := []ExceptionItem{{Type: "Error", Value: "boom"}}
	for _, msg := range []Message{
		Capture{Event: "mcp", DistinctId: "d", Library: "posthog-go-mcp"},
		Capture{Event: "plain", DistinctId: "d"},
		Exception{DistinctId: "d", Library: "posthog-go-mcp", ExceptionList: item},
		Exception{DistinctId: "d", ExceptionList: item},
		Identify{DistinctId: "d"},
	} {
		require.NoError(t, client.Enqueue(msg))
	}
	require.NoError(t, client.Close())

	var wireLibs []interface{}
	for _, message := range readBatch(t, body)["batch"].([]interface{}) {
		wireLibs = append(wireLibs, message.(map[string]interface{})["properties"].(map[string]interface{})["$lib"])
	}
	require.Equal(t, []interface{}{nil, nil, nil, nil, nil}, wireLibs)
	require.Equal(t, []string{"posthog-go-mcp", "", "posthog-go-mcp", ""}, seenByHook)
}

// Capture v1 carries SDK identity in the per-request PostHog-Sdk-Info header,
// which the server stamps over any body $lib, so Library cannot apply there.
func TestV1IdentifiesSDKPerRequestNotPerEvent(t *testing.T) {
	type request struct {
		sdkInfo string
		libs    []interface{}
	}
	requests := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var env struct {
			Batch []struct {
				Uuid       string                 `json:"uuid"`
				Properties map[string]interface{} `json:"properties"`
			} `json:"batch"`
		}
		require.NoError(t, json.Unmarshal(raw, &env))
		got := request{sdkInfo: r.Header.Get("PostHog-Sdk-Info")}
		results := map[string]eventResult{}
		for _, ev := range env.Batch {
			got.libs = append(got.libs, ev.Properties["$lib"])
			results[ev.Uuid] = eventResult{Result: resultOk}
		}
		requests <- got
		_, _ = w.Write([]byte(resultsBody(t, results)))
	}))
	defer server.Close()

	client, err := NewWithConfig("phc_test", Config{
		Endpoint:  server.URL,
		BatchSize: 10,
		Logger:    toLogger(t),
	})
	require.NoError(t, err)
	require.NoError(t, client.Enqueue(Capture{Event: "mcp", DistinctId: "d", Library: "posthog-go-mcp"}))
	require.NoError(t, client.Enqueue(Capture{Event: "plain", DistinctId: "d"}))
	require.NoError(t, client.Close())

	require.Equal(t, request{sdkInfo: "posthog-go/1.0.0", libs: []interface{}{nil, nil}}, <-requests)
}
