package mcp

import (
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
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
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
	err := New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		DistinctID: strings.Repeat("x", maxEventBytes),
	})
	require.ErrorContains(t, err, "required tool-call event exceeds")
	assert.Empty(t, client.messages)
}

func TestCaptureToolCallFieldLimits(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
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
