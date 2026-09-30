package posthogmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateUTF8IncludesMarker(t *testing.T) {
	got := truncateUTF8(strings.Repeat("🙂", 10), 10)
	assert.LessOrEqual(t, len(got), 10)
	assert.True(t, utf8.ValidString(got))
	assert.True(t, strings.HasSuffix(got, truncationSuffix))

	invalid := string([]byte{'a', 0xff, 'b'})
	assert.True(t, utf8.ValidString(truncateUTF8(invalid, 100)))
}

func TestTruncateValueDepthAndBreadth(t *testing.T) {
	deep := any("value")
	for i := 0; i < maxDepth+1; i++ {
		deep = map[string]any{"next": deep}
	}
	truncated := truncateValue(deep).(map[string]any)
	for i := 0; i < maxDepth-1; i++ {
		truncated = truncated["next"].(map[string]any)
	}
	assert.Equal(t, "[Object]", truncated["next"])

	wide := make(map[string]any, maxBreadth+1)
	for i := 0; i < maxBreadth+1; i++ {
		wide[fmt.Sprintf("key-%03d", i)] = i
	}
	got := truncateValue(wide).(map[string]any)
	assert.Len(t, got, maxBreadth)
	assert.Equal(t, "[MaxProperties ~]", got["..."])
}

func TestCaptureToolCallDeterministicSizePruning(t *testing.T) {
	large := strings.Repeat("x:", maxStringBytes/2)
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName: "query",
		Parameters: map[string]any{
			"a": large,
			"b": large,
			"c": large,
			"d": large,
		},
		Response: map[string]any{
			"a": large,
			"b": large,
			"c": large,
			"d": large,
		},
		Properties: posthog.Properties{
			"custom_a": large,
			"custom_b": large,
			"custom_c": large,
			"custom_d": large,
		},
	}))
	capture := requireCapture(t, client.messages[0])
	assert.NotContains(t, capture.Properties, propertyResponse)
	assert.NotContains(t, capture.Properties, propertyParameters)
	assert.NotContains(t, capture.Properties, "custom_a")
	assert.Equal(t, "query", capture.Properties[propertyToolName])
	size, err := messageSize(capture)
	require.NoError(t, err)
	assert.LessOrEqual(t, size, maxEventBytes)
}

func TestCaptureToolCallIrreducibleOversizeFailsBeforeEnqueue(t *testing.T) {
	client := &fakeEnqueueClient{}
	err := New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName:   "query",
		DistinctID: strings.Repeat("x", maxEventBytes),
	})
	require.ErrorContains(t, err, "required tool-call event exceeds")
	assert.Empty(t, client.messages)
}

func TestCaptureToolCallFieldLimits(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName:        strings.Repeat("🙂", maxResourceNameBytes),
		ToolDescription: strings.Repeat("🙂", maxStringBytes),
		ToolCategory:    strings.Repeat("🙂", maxMetadataBytes),
		Intent:          strings.Repeat("🙂", maxIntentBytes),
		IsError:         true,
		Error:           errorsWithMessage(strings.Repeat("🙂", maxErrorMessageBytes)),
	}))
	capture := requireCapture(t, client.messages[0])
	for key, limit := range map[string]int{
		propertyToolName:        maxResourceNameBytes,
		propertyToolDescription: maxStringBytes,
		propertyToolCategory:    maxMetadataBytes,
		propertyIntent:          maxIntentBytes,
		propertyErrorMessage:    maxErrorMessageBytes,
	} {
		value := capture.Properties[key].(string)
		assert.LessOrEqual(t, len(value), limit, key)
		assert.True(t, utf8.ValidString(value), key)
	}
}

type errorsWithMessage string

func (e errorsWithMessage) Error() string { return string(e) }

func TestCaptureToolCallSizePruningStages(t *testing.T) {
	large := strings.Repeat("x:", 15_000)
	oversized := map[string]any{"a": large, "b": large, "c": large, "d": large}
	oversizedProperties := posthog.Properties{"a": large, "b": large, "c": large, "d": large}
	strings30 := make([]any, 30)
	for i := range strings30 {
		strings30[i] = large
	}
	small := map[string]any{"q": "x"}

	tests := []struct {
		name           string
		call           ToolCall
		wantResponse   any
		wantParameters any
		wantCustom     any
		wantSet        any
	}{
		{
			name:           "fits untouched",
			call:           ToolCall{Response: small, Parameters: small, Properties: posthog.Properties{"c": "x"}, SetProperties: posthog.Properties{"s": "x"}},
			wantResponse:   map[string]any{"q": "x"},
			wantParameters: map[string]any{"q": "x"},
			wantCustom:     "x",
			wantSet:        posthog.Properties{"s": "x"},
		},
		{
			name:           "depth shrinks until it fits",
			call:           ToolCall{Response: map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": strings30}}}}, Parameters: small},
			wantResponse:   map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "[Array]"}}}},
			wantParameters: map[string]any{"q": "x"},
		},
		{
			name:           "response dropped first",
			call:           ToolCall{Response: oversized, Parameters: small, Properties: posthog.Properties{"c": "x"}, SetProperties: posthog.Properties{"s": "x"}},
			wantParameters: map[string]any{"q": "x"},
			wantCustom:     "x",
			wantSet:        posthog.Properties{"s": "x"},
		},
		{
			name:       "parameters dropped next",
			call:       ToolCall{Response: oversized, Parameters: oversized, Properties: posthog.Properties{"c": "x"}, SetProperties: posthog.Properties{"s": "x"}},
			wantCustom: "x",
			wantSet:    posthog.Properties{"s": "x"},
		},
		{
			name:    "custom properties dropped next",
			call:    ToolCall{Response: oversized, Parameters: oversized, Properties: oversizedProperties, SetProperties: posthog.Properties{"s": "x"}},
			wantSet: posthog.Properties{"s": "x"},
		},
		{
			name: "person properties dropped last",
			call: ToolCall{Response: oversized, Parameters: oversized, Properties: oversizedProperties, SetProperties: posthog.Properties(oversized)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			tt.call.ToolName = "query"
			tt.call.DistinctID = "user_1"
			require.NoError(t, New(client).CaptureToolCall(context.Background(), tt.call))

			properties := requireCapture(t, client.messages[0]).Properties
			assert.Equal(t, tt.wantResponse, properties[propertyResponse])
			assert.Equal(t, tt.wantParameters, properties[propertyParameters])
			assert.Equal(t, tt.wantCustom, properties["c"])
			assert.Equal(t, tt.wantSet, properties[propertySet])
		})
	}
}

func TestCaptureToolCallBoundsParametersAndResponseOnce(t *testing.T) {
	longString := strings.Repeat("x:", maxStringBytes)
	wideList := make([]any, maxBreadth+50)
	wideMap := make(map[string]any, maxBreadth+50)
	for i := range wideList {
		wideList[i] = i
		wideMap[fmt.Sprintf("key-%03d", i)] = i
	}
	deep := any("leaf")
	for i := 0; i < maxDepth+2; i++ {
		deep = map[string]any{"next": deep}
	}

	tests := []struct {
		name  string
		value any
		check func(t *testing.T, got any)
	}{
		{"long string", longString, func(t *testing.T, got any) {
			value := got.(string)
			assert.Len(t, value, maxStringBytes)
			assert.True(t, strings.HasSuffix(value, "..."))
		}},
		{"wide list", wideList, func(t *testing.T, got any) {
			list := got.([]any)
			assert.Len(t, list, maxBreadth)
			assert.Equal(t, "[MaxProperties ~]", list[maxBreadth-1])
			assert.Equal(t, json.Number("98"), list[98])
		}},
		{"wide map", wideMap, func(t *testing.T, got any) {
			object := got.(map[string]any)
			assert.Len(t, object, maxBreadth)
			assert.Equal(t, "[MaxProperties ~]", object["..."])
			assert.Equal(t, json.Number("98"), object["key-098"])
			assert.NotContains(t, object, "key-099")
		}},
		{"deep map", deep, func(t *testing.T, got any) {
			object := got.(map[string]any)
			for i := 0; i < maxDepth-1; i++ {
				object = object["next"].(map[string]any)
			}
			assert.Equal(t, "[Object]", object["next"])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
				ToolName:   "query",
				Parameters: tt.value,
				Response:   tt.value,
			}))

			properties := requireCapture(t, client.messages[0]).Properties
			tt.check(t, properties[propertyParameters])
			tt.check(t, properties[propertyResponse])
		})
	}
}
