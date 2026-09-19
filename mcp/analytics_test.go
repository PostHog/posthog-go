package mcp

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEnqueueClient struct {
	messages []posthog.Message
	errors   []error
}

func (f *fakeEnqueueClient) Enqueue(message posthog.Message) error {
	f.messages = append(f.messages, message)
	index := len(f.messages) - 1
	if index < len(f.errors) {
		return f.errors[index]
	}
	return nil
}

func TestCaptureToolCallMinimal(t *testing.T) {
	client := &fakeEnqueueClient{}
	analytics := New(client)

	require.NoError(t, analytics.CaptureToolCall(ToolCall{ToolName: "search_docs"}))
	require.Len(t, client.messages, 1)

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "anonymous", capture.DistinctId)
	assert.Equal(t, eventToolCall, capture.Event)
	assert.Equal(t, analyticsSource, capture.Properties[propertySource])
	assert.Equal(t, "search_docs", capture.Properties[propertyResourceName])
	assert.Equal(t, "search_docs", capture.Properties[propertyToolName])
	assert.Equal(t, float64(0), capture.Properties[propertyDurationMS])
	assert.Equal(t, false, capture.Properties[propertyIsError])
	assert.Equal(t, false, capture.Properties[propertyProcessProfile])
	assert.NotContains(t, capture.Properties, propertySessionID)
	assert.NotContains(t, capture.Properties, propertySet)
	assert.Nil(t, capture.Groups)
}

func TestCaptureToolCallCompleteMappingAndPrecedence(t *testing.T) {
	client := &fakeEnqueueClient{}
	analytics := New(client)
	parameters := map[string]any{"query": "select 1"}
	response := map[string]any{"rows": []any{1, 2}}
	groups := posthog.Groups{"organization": "org_1"}
	setProperties := posthog.Properties{"plan": "pro"}
	custom := posthog.Properties{
		propertyClientName:     "custom-client",
		propertyGroups:         map[string]any{"organization": "wrong"},
		propertySet:            map[string]any{"plan": "wrong"},
		propertyProcessProfile: false,
		"environment":          "test",
	}

	require.NoError(t, analytics.CaptureToolCall(ToolCall{
		ToolName:        "query",
		ToolDescription: "Run a query",
		ToolCategory:    "Data",
		DistinctID:      "user_1",
		SessionID:       "session_1",
		Groups:          groups,
		SetProperties:   setProperties,
		ServerName:      "server",
		ServerVersion:   "1.0",
		ClientName:      "client",
		ClientVersion:   "2.0",
		ProtocolVersion: "2026-07-28",
		Intent:          "  inspect data  ",
		Parameters:      parameters,
		Response:        response,
		Duration:        1500 * time.Microsecond,
		Properties:      custom,
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "user_1", capture.DistinctId)
	assert.Equal(t, float64(1.5), capture.Properties[propertyDurationMS])
	assert.Equal(t, "custom-client", capture.Properties[propertyClientName])
	assert.Equal(t, "inspect data", capture.Properties[propertyIntent])
	assert.Equal(t, string(IntentSourceContextParameter), capture.Properties[propertyIntentSource])
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, capture.Groups)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, capture.Properties[propertyGroups])
	assert.Equal(t, posthog.Properties{"plan": "pro"}, capture.Properties[propertySet])
	assert.NotContains(t, capture.Properties, propertyProcessProfile)
	assert.Equal(t, "test", capture.Properties["environment"])

	assert.Equal(t, map[string]any{"query": "select 1"}, parameters)
	assert.Equal(t, map[string]any{"rows": []any{1, 2}}, response)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, groups)
	assert.Equal(t, posthog.Properties{"plan": "pro"}, setProperties)
	assert.Equal(t, false, custom[propertyProcessProfile])
}

func TestCaptureToolCallSessionFallbackAndAnonymousSetSuppression(t *testing.T) {
	for _, test := range []struct {
		name       string
		call       ToolCall
		distinctID string
		personless bool
	}{
		{
			name:       "session fallback",
			call:       ToolCall{ToolName: "tool", SessionID: "session_1", SetProperties: posthog.Properties{"email": "ignored"}},
			distinctID: "session_1",
			personless: true,
		},
		{
			name:       "anonymous fallback",
			call:       ToolCall{ToolName: "tool", SetProperties: posthog.Properties{"email": "ignored"}},
			distinctID: "anonymous",
			personless: true,
		},
		{
			name:       "explicit identity",
			call:       ToolCall{ToolName: "tool", DistinctID: "user_1"},
			distinctID: "user_1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(test.call))
			capture := requireCapture(t, client.messages[0])
			assert.Equal(t, test.distinctID, capture.DistinctId)
			assert.NotContains(t, capture.Properties, propertySet)
			if test.personless {
				assert.Equal(t, false, capture.Properties[propertyProcessProfile])
			} else {
				assert.NotContains(t, capture.Properties, propertyProcessProfile)
			}
		})
	}
}

func TestCaptureToolCallValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		call ToolCall
		want string
	}{
		{name: "empty name", call: ToolCall{}, want: "ToolName"},
		{name: "blank name", call: ToolCall{ToolName: "  \t"}, want: "ToolName"},
		{name: "negative duration", call: ToolCall{ToolName: "tool", Duration: -1}, want: "Duration"},
		{name: "invalid intent source", call: ToolCall{ToolName: "tool", IntentSource: "model"}, want: "IntentSource"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			err := New(client).CaptureToolCall(test.call)
			require.ErrorContains(t, err, test.want)
			assert.Empty(t, client.messages)
		})
	}

	require.ErrorContains(t, (*Analytics)(nil).CaptureToolCall(ToolCall{ToolName: "tool"}), "nil enqueue client")
	require.ErrorContains(t, New(nil).CaptureToolCall(ToolCall{ToolName: "tool"}), "nil enqueue client")
}

type panickingError struct{}

func (panickingError) Error() string { panic("should not run") }

func TestCaptureToolCallFailureAndException(t *testing.T) {
	client := &fakeEnqueueClient{}
	token := "phc_abcdefghijklmnopqrstuvwxyz"
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		DistinctID: "user_1",
		Groups:     posthog.Groups{"organization": "org_1"},
		IsError:    true,
		Error:      errors.New("request failed with " + token),
		ErrorType:  "validation",
	}))
	require.Len(t, client.messages, 2)

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, true, capture.Properties[propertyIsError])
	assert.Equal(t, "validation", capture.Properties[propertyErrorType])
	assert.Equal(t, "request failed with [redacted]", capture.Properties[propertyErrorMessage])

	exception := requireException(t, client.messages[1])
	require.Len(t, exception.ExceptionList, 1)
	item := exception.ExceptionList[0]
	assert.Equal(t, "validation", item.Type)
	assert.Equal(t, "request failed with [redacted]", item.Value)
	require.NotNil(t, item.Mechanism)
	assert.Equal(t, true, *item.Mechanism.Handled)
	assert.Equal(t, true, *item.Mechanism.Synthetic)
	assert.Nil(t, item.Stacktrace)
	assert.Equal(t, posthog.Groups{"organization": "org_1"}, exception.Properties[propertyGroups])
}

func TestCaptureToolCallFailureDefaultsAndDisableFanout(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client, WithExceptionAutocapture(false)).CaptureToolCall(ToolCall{
		ToolName: "query",
		IsError:  true,
	}))
	require.Len(t, client.messages, 1)
	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "Error", capture.Properties[propertyErrorType])
	assert.Equal(t, "Tool query returned an error", capture.Properties[propertyErrorMessage])

	client = &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName: "query",
		Error:    panickingError{},
	}))
	assert.Len(t, client.messages, 1, "successful calls must ignore Error")
}

func TestCaptureToolCallPanickingErrorFailsWithoutEnqueue(t *testing.T) {
	client := &fakeEnqueueClient{}
	err := New(client).CaptureToolCall(ToolCall{ToolName: "query", IsError: true, Error: panickingError{}})
	require.ErrorContains(t, err, "Error method panicked")
	assert.Empty(t, client.messages)
}

func TestCaptureToolCallAttemptsAllEnqueues(t *testing.T) {
	client := &fakeEnqueueClient{errors: []error{errors.New("capture full"), errors.New("exception full")}}
	err := New(client).CaptureToolCall(ToolCall{ToolName: "query", IsError: true})
	require.Error(t, err)
	assert.ErrorContains(t, err, "enqueue $mcp_tool_call: capture full")
	assert.ErrorContains(t, err, "enqueue $exception: exception full")
	assert.Len(t, client.messages, 2)
}

func TestCaptureToolCallNormalizationErrorsAreSafe(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	secret := strings.Repeat("do-not-return", 100)

	for _, value := range []any{
		func() {},
		cycle,
		map[string]any{"number": math.Inf(1), "secret": secret},
		panickingJSON{},
	} {
		client := &fakeEnqueueClient{}
		err := New(client).CaptureToolCall(ToolCall{ToolName: "query", Parameters: value})
		require.Error(t, err)
		assert.LessOrEqual(t, len(err.Error()), maxReturnedErrorBytes)
		assert.NotContains(t, err.Error(), secret)
		assert.Empty(t, client.messages)
	}
}

type panickingJSON struct{}

func (panickingJSON) MarshalJSON() ([]byte, error) { panic("secret payload") }

func TestCaptureToolCallWireGolden(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client, WithExceptionAutocapture(false)).CaptureToolCall(ToolCall{
		ToolName:        "search_docs",
		ToolDescription: "Search documentation",
		ToolCategory:    "Docs",
		DistinctID:      "user_1",
		SessionID:       "session_1",
		Groups:          posthog.Groups{"organization": "org_1"},
		SetProperties:   posthog.Properties{"plan": "pro"},
		ServerName:      "docs-server",
		ServerVersion:   "1.0.0",
		ClientName:      "test-client",
		ClientVersion:   "2.0.0",
		ProtocolVersion: "2026-07-28",
		Intent:          "Find setup instructions",
		IntentSource:    IntentSourceInferred,
		Parameters:      map[string]any{"query": "setup"},
		Response:        map[string]any{"content": []any{map[string]any{"type": "text", "text": "Found"}}},
		Duration:        42 * time.Millisecond,
		Properties:      posthog.Properties{"environment": "test"},
		Timestamp:       time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC),
	}))

	actual := normalizedCaptureWire(t, requireCapture(t, client.messages[0]))
	expected, err := os.ReadFile("testdata/tool_call.golden.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(actual))
}

func normalizedCaptureWire(t *testing.T, capture posthog.Capture) []byte {
	t.Helper()
	data, err := json.Marshal(capture.APIfy())
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	delete(wire, "uuid")
	delete(wire, "timestamp")
	properties := wire["properties"].(map[string]any)
	for _, key := range []string{"$lib", "$lib_version", "$go_version", "$os", "$os_version", "$os_distro", "$is_server"} {
		delete(properties, key)
	}
	result, err := json.MarshalIndent(wire, "", "  ")
	require.NoError(t, err)
	return result
}

func requireCapture(t *testing.T, message posthog.Message) posthog.Capture {
	t.Helper()
	capture, ok := message.(posthog.Capture)
	require.True(t, ok, "message type = %T", message)
	return capture
}

func requireException(t *testing.T, message posthog.Message) posthog.Exception {
	t.Helper()
	exception, ok := message.(posthog.Exception)
	require.True(t, ok, "message type = %T", message)
	return exception
}
