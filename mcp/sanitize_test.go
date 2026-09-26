package mcp

import (
	"errors"
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

func TestCaptureToolCallCustomPropertiesCannotReplaceCanonicalFields(t *testing.T) {
	client := &fakeEnqueueClient{}
	properties := posthog.Properties{
		propertyIntent:   "alice@example.com",
		propertyResponse: map[string]any{"content": []any{map[string]any{"type": "image", "data": "raw image"}}},
		propertyIsError:  true,
		"environment":    "test",
	}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		Intent:     "Find alice@example.com",
		Response:   map[string]any{"content": []any{map[string]any{"type": "image", "data": "raw image"}}},
		Properties: properties,
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "Find [redacted]", capture.Properties[propertyIntent])
	assert.Equal(t, false, capture.Properties[propertyIsError])
	assert.Equal(t, "test", capture.Properties["environment"])
	content := capture.Properties[propertyResponse].(map[string]any)["content"].([]any)
	assert.Equal(t, "[image content redacted - not supported by PostHog MCP analytics]", content[0].(map[string]any)["text"])
	assert.NotContains(t, content[0], "data")
	assert.Equal(t, "alice@example.com", properties[propertyIntent])
}

func TestCaptureToolCallRedactsStructuredIntentIdentifiers(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent string
		want   string
	}{
		{
			name:   "email and ipv4",
			intent: "Find orders for alice@example.com from 192.0.2.1",
			want:   "Find orders for [redacted] from [redacted]",
		},
		{
			name:   "email keeps surrounding punctuation",
			intent: "Reach alice@example.com.",
			want:   "Reach [redacted].",
		},
		{
			name:   "ipv6 full and compressed",
			intent: "route 2001:0db8:85a3:0000:0000:8a2e:0370:7334 via 2001:db8::8a2e:1 and ::1 or 2001:db8::",
			want:   "route [redacted] via [redacted] and [redacted] or [redacted]",
		},
		{
			name:   "address shaped compressed ipv6",
			intent: "ping dead::beef now",
			want:   "ping [redacted] now",
		},
		{
			name:   "luhn card separators and expiry",
			intent: "pay 4111111111111111 or 4111-1111-1111-1111 or 4111 1111 1111 1111 12/30",
			want:   "pay [redacted] or [redacted] or [redacted] 12/30",
		},
		{
			name:   "two cards",
			intent: "4111111111111111 and 5500000000000004",
			want:   "[redacted] and [redacted]",
		},
		{
			name:   "us ssn separators",
			intent: "ssn 123-45-6789 / 123 45 6789 / 123.45.6789",
			want:   "ssn [redacted] / [redacted] / [redacted]",
		},
		{
			name:   "unicode separators",
			intent: "card 4111\u00a01111\u00a01111\u00a01111 ssn 123\u202f45\u202f6789 phone (415)\u00a0555-0142",
			want:   "card [redacted] ssn [redacted] phone [redacted]",
		},
		{
			name:   "nanp and international phones",
			intent: "call (415) 555-0142, (415)555-0142, 415-555-0142, or +44 20 7946 0958",
			want:   "call [redacted], [redacted], [redacted], or [redacted]",
		},
		{
			name:   "false positives stay",
			intent: "caught std::bad_alloc in version 1.2.3 on 2024-01-15 12:30 id 123456789 phone 4155550142 card 4111 1111 1111 1112 for Alice",
			want:   "caught std::bad_alloc in version 1.2.3 on 2024-01-15 12:30 id 123456789 phone 4155550142 card 4111 1111 1111 1112 for Alice",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(ToolCall{
				ToolName:   "query",
				DistinctID: "user_1",
				Intent:     test.intent,
			}))
			capture := requireCapture(t, client.messages[0])
			assert.Equal(t, test.want, capture.Properties[propertyIntent])
		})
	}
}

func TestCaptureToolCallRedactsIntentBeforeTruncation(t *testing.T) {
	const email = "alice@example.com"
	// '|' is outside the email local-part alphabet, so redaction cannot absorb
	// the prefix. The address still straddles the truncation cut if redaction
	// runs second.
	prefix := strings.Repeat("|", 2036)
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		DistinctID: "user_1",
		Intent:     prefix + email,
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, prefix+redactedValue, capture.Properties[propertyIntent])
}

func TestCaptureToolCallLeavesNonIntentIdentifiersUntouched(t *testing.T) {
	raw := "Find orders for alice@example.com from 192.0.2.1"
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(ToolCall{
		ToolName:   "query",
		DistinctID: "user_1",
		Intent:     raw,
		Parameters: map[string]any{"note": raw},
		Response:   map[string]any{"text": raw},
		IsError:    true,
		Error:      errors.New("request failed for alice@example.com"),
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "Find orders for [redacted] from [redacted]", capture.Properties[propertyIntent])
	assert.Equal(t, raw, capture.Properties[propertyParameters].(map[string]any)["note"])
	assert.Equal(t, raw, capture.Properties[propertyResponse].(map[string]any)["text"])
	assert.Equal(t, "request failed for alice@example.com", capture.Properties[propertyErrorMessage])
}

func TestSanitizeStringBase64Variants(t *testing.T) {
	assert.Equal(t, binaryRedactedValue, sanitizeString(strings.Repeat("A", largeBinaryGateBytes)))
	assert.Equal(t, binaryRedactedValue, sanitizeString(strings.Repeat("a_", largeBinaryGateBytes/2)))
	assert.Equal(t, binaryRedactedValue, sanitizeString("data:image/png;base64,"+strings.Repeat("A", largeBinaryGateBytes)))
	assert.Equal(t, strings.Repeat("A", largeBinaryGateBytes-1), sanitizeString(strings.Repeat("A", largeBinaryGateBytes-1)))
}
