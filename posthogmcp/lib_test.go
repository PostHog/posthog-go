package posthogmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/posthog/posthog-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// messageProperties returns the properties an MCP message hands to the core
// SDK, before any SDK fill.
func messageProperties(t *testing.T, message posthog.Message) posthog.Properties {
	t.Helper()
	switch m := message.(type) {
	case posthog.Capture:
		return m.Properties
	case posthog.Exception:
		return m.Properties
	}
	t.Fatalf("unexpected message %T", message)
	return nil
}

func TestMCPEventsCarryMCPLibrary(t *testing.T) {
	// absent marks a $lib_version that must not be set.
	const absent = "<absent>"
	for name, test := range map[string]struct {
		properties  posthog.Properties
		context     posthog.Properties
		wantLib     any
		wantVersion any
	}{
		"no caller properties":          {wantLib: library, wantVersion: posthog.Version},
		"caller $lib alone":             {properties: posthog.Properties{"$lib": "caller-lib"}, wantLib: "caller-lib", wantVersion: absent},
		"caller pair":                   {properties: posthog.Properties{"$lib": "caller-lib", "$lib_version": "9.9.9"}, wantLib: "caller-lib", wantVersion: "9.9.9"},
		"caller mcp $lib alone":         {properties: posthog.Properties{"$lib": library}, wantLib: library, wantVersion: posthog.Version},
		"caller lone $lib_version":      {properties: posthog.Properties{"$lib_version": "9.9.9"}, wantLib: library, wantVersion: posthog.Version},
		"caller nil $lib":               {properties: posthog.Properties{"$lib": nil, "$lib_version": "9.9.9"}, wantLib: library, wantVersion: posthog.Version},
		"caller empty $lib":             {properties: posthog.Properties{"$lib": "", "$lib_version": "9.9.9"}, wantLib: library, wantVersion: posthog.Version},
		"caller non-string $lib":        {properties: posthog.Properties{"$lib": 5, "$lib_version": "9.9.9"}, wantLib: library, wantVersion: posthog.Version},
		"request context $lib":          {context: posthog.Properties{"$lib": "context-lib", "$lib_version": "8.8.8"}, wantLib: "context-lib", wantVersion: "8.8.8"},
		"caller $lib beats context":     {properties: posthog.Properties{"$lib": "caller-lib"}, context: posthog.Properties{"$lib": "context-lib", "$lib_version": "8.8.8"}, wantLib: "caller-lib", wantVersion: "8.8.8"},
		"caller nil $lib beats context": {properties: posthog.Properties{"$lib": nil}, context: posthog.Properties{"$lib": "context-lib"}, wantLib: library, wantVersion: posthog.Version},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if test.context != nil {
				ctx = posthog.WithRequestContext(ctx, posthog.RequestContext{Properties: test.context})
			}
			client := &fakeEnqueueClient{}
			analytics := New(client)
			require.NoError(t, analytics.CaptureToolCall(ctx, ToolCall{
				ToolName:   "query",
				Error:      errors.New("boom"),
				Properties: test.properties,
			}))
			require.NoError(t, analytics.CaptureUnknownTool(ctx, UnknownTool{
				EventContext: EventContext{Properties: test.properties},
				ToolName:     "missing",
			}))
			require.Len(t, client.messages, 3, "tool call, exception, optional event")
			for _, message := range client.messages {
				properties := messageProperties(t, message)
				assert.Equal(t, test.wantLib, properties["$lib"], "%T $lib", message)
				if test.wantVersion == absent {
					assert.NotContains(t, properties, "$lib_version", "%T $lib_version", message)
				} else {
					assert.Equal(t, test.wantVersion, properties["$lib_version"], "%T $lib_version", message)
				}
			}
		})
	}
}

func TestMCPLibraryOnWireAndBeforeSend(t *testing.T) {
	for name, test := range map[string]struct {
		hook        func(posthog.Properties)
		wantLib     any
		wantVersion any
	}{
		"unchanged": {wantLib: library, wantVersion: posthog.Version},
		"changed":   {hook: func(p posthog.Properties) { p.Set("$lib", "hook-lib").Set("$lib_version", "hook-v") }, wantLib: "hook-lib", wantVersion: "hook-v"},
		"removed":   {hook: func(p posthog.Properties) { delete(p, "$lib"); delete(p, "$lib_version") }},
	} {
		t.Run(name, func(t *testing.T) {
			payloads := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				payloads <- body
				var request struct {
					Batch []struct {
						UUID string `json:"uuid"`
					} `json:"batch"`
				}
				results := map[string]any{}
				if json.Unmarshal(body, &request) == nil {
					for _, event := range request.Batch {
						results[event.UUID] = map[string]string{"result": "ok"}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
			}))
			defer server.Close()

			var seenLib, seenVersion any
			client, err := posthog.NewWithConfig("test-key", posthog.Config{
				Endpoint:               server.URL,
				BatchSize:              1,
				DefaultEventProperties: posthog.Properties{"$lib": "default-lib", "$lib_version": "default-v"},
				BeforeSend: func(msg posthog.Message) posthog.Message {
					capture := msg.(posthog.Capture)
					seenLib, seenVersion = capture.Properties["$lib"], capture.Properties["$lib_version"]
					if test.hook != nil {
						capture.Properties = posthog.NewProperties().Merge(capture.Properties)
						test.hook(capture.Properties)
					}
					return capture
				},
			})
			require.NoError(t, err)
			defer client.Close()

			require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", DistinctID: "user_1"}))

			var body []byte
			select {
			case body = <-payloads:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the capture request")
			}
			var payload struct {
				Batch []struct {
					Properties map[string]any `json:"properties"`
				} `json:"batch"`
			}
			require.NoError(t, json.Unmarshal(body, &payload))
			require.Len(t, payload.Batch, 1)

			assert.Equal(t, library, seenLib, "the MCP label beats DefaultEventProperties")
			assert.Equal(t, posthog.Version, seenVersion, "the MCP label is a pair")
			properties := payload.Batch[0].Properties
			if test.wantLib == nil {
				assert.NotContains(t, properties, "$lib")
				assert.NotContains(t, properties, "$lib_version")
				return
			}
			assert.Equal(t, test.wantLib, properties["$lib"])
			assert.Equal(t, test.wantVersion, properties["$lib_version"])
		})
	}
}
