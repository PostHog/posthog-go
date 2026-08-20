package mcp

import (
	"strings"
	"testing"

	posthog "github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureToolCallSanitizesPayloadsWithoutMutation(t *testing.T) {
	token := "phc_abcdefghijklmnopqrstuvwxyz"
	binary := strings.Repeat("A", largeBinaryGateBytes)
	parameters := map[string]any{
		"authorization": "Bearer secret",
		"nested": map[string]any{
			"api_key": "secret",
			"query":   "token " + token,
			"binary":  binary,
		},
	}
	response := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "token " + token},
			map[string]any{"type": "image", "data": "raw", "mimeType": "image/png"},
			map[string]any{"type": "audio", "data": "raw"},
			map[string]any{"type": "resource", "resource": map[string]any{"blob": "raw"}},
			map[string]any{"type": "resource_link", "uri": "https://example.test/" + token},
			map[string]any{"type": "video", "data": "raw"},
		},
	}
	properties := posthog.Properties{"password": "secret", "token_value": token}

	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		Parameters: parameters,
		Response:   response,
		Properties: properties,
	}))
	capture := requireCapture(t, client.messages[0])

	gotParameters := capture.Properties[propertyParameters].(map[string]any)
	assert.Equal(t, redactedValue, gotParameters["authorization"])
	nested := gotParameters["nested"].(map[string]any)
	assert.Equal(t, redactedValue, nested["api_key"])
	assert.Equal(t, "token [redacted]", nested["query"])
	assert.Equal(t, binaryRedactedValue, nested["binary"])
	assert.Equal(t, redactedValue, capture.Properties["password"])
	assert.Equal(t, redactedValue, capture.Properties["token_value"])

	gotResponse := capture.Properties[propertyResponse].(map[string]any)
	content := gotResponse["content"].([]any)
	assert.Equal(t, "token [redacted]", content[0].(map[string]any)["text"])
	assert.Equal(t, "[image content redacted - not supported by PostHog MCP analytics]", content[1].(map[string]any)["text"])
	assert.Equal(t, "[audio content redacted - not supported by PostHog MCP analytics]", content[2].(map[string]any)["text"])
	assert.Equal(t, "[binary resource content redacted - not supported by PostHog MCP analytics]", content[3].(map[string]any)["text"])
	assert.Equal(t, "https://example.test/[redacted]", content[4].(map[string]any)["uri"])
	assert.Equal(t, `[unsupported content type "video" redacted - not supported by PostHog MCP analytics]`, content[5].(map[string]any)["text"])

	assert.Equal(t, "Bearer secret", parameters["authorization"])
	assert.Equal(t, "secret", parameters["nested"].(map[string]any)["api_key"])
	assert.Equal(t, "raw", response["content"].([]any)[1].(map[string]any)["data"])
	assert.Equal(t, "secret", properties["password"])
}

func TestSanitizeStringBase64Variants(t *testing.T) {
	assert.Equal(t, binaryRedactedValue, sanitizeString(strings.Repeat("A", largeBinaryGateBytes)))
	assert.Equal(t, binaryRedactedValue, sanitizeString(strings.Repeat("a_", largeBinaryGateBytes/2)))
	assert.Equal(t, binaryRedactedValue, sanitizeString("data:image/png;base64,"+strings.Repeat("A", largeBinaryGateBytes)))
	assert.Equal(t, strings.Repeat("A", largeBinaryGateBytes-1), sanitizeString(strings.Repeat("A", largeBinaryGateBytes-1)))
}
