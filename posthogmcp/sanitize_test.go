package posthogmcp

import (
	"context"
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
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
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

func TestCaptureToolCallRedactsMediaBeforeNormalization(t *testing.T) {
	response := map[string]any{
		"content": []any{map[string]any{"type": "image", "data": panickingJSON{}}},
	}
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", Response: response}))

	capture := requireCapture(t, client.messages[0])
	content := capture.Properties[propertyResponse].(map[string]any)["content"].([]any)
	assert.Equal(t, "[image content redacted - not supported by PostHog MCP analytics]", content[0].(map[string]any)["text"])
	assert.IsType(t, panickingJSON{}, response["content"].([]any)[0].(map[string]any)["data"])
}

func TestCaptureToolCallOmitsOversizedRawResponse(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName: "query",
		Response: map[string]any{"content": []any{map[string]any{
			"type": "text", "text": strings.Repeat("x", maxNormalizeBytes),
		}}},
	}))
	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, oversizedPayloadValue, capture.Properties[propertyResponse])
}

func TestCaptureToolCallCustomPropertiesCannotReplaceCanonicalFields(t *testing.T) {
	client := &fakeEnqueueClient{}
	properties := posthog.Properties{
		propertyIntent:   "alice@example.com",
		propertyResponse: map[string]any{"content": []any{map[string]any{"type": "image", "data": "raw image"}}},
		propertyIsError:  true,
		"environment":    "test",
	}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
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
			require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
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
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
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
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
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

func TestCaptureToolCallRedactsCredentialsInCapturedText(t *testing.T) {
	key := "sk-proj-" + "T3BlbkFJabcd1234efgh5678ijkl9012mnop3456qrst7890wxyz"
	prose := "user 550e8400-e29b-41d4-a716-446655440000 not found in /usr/local/lib/app.py at v1.2.3-beta.4 (sha d41d8cd98f00b204e9800998ecf8427e)"
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName:   "query",
		Parameters: map[string]any{"note": "auth with " + key, "prose": prose},
		Response: map[string]any{"content": []any{map[string]any{
			"type": "text", "text": "fetched https://svc:pw@internal.test/doc?sig=abc, then parsed",
		}}},
		Properties: posthog.Properties{"callback": "https://example.com/cb#access_token=abc&state=xyz"},
		IsError:    true,
		Error:      errors.New("GET https://example.com/x?token=abc failed: auth with " + key + " rejected"),
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, map[string]any{"note": "auth with [redacted]", "prose": prose}, capture.Properties[propertyParameters])
	content := capture.Properties[propertyResponse].(map[string]any)["content"].([]any)
	assert.Equal(t, "fetched https://%5Bredacted%5D@internal.test/doc?sig=%5Bredacted%5D then parsed", content[0].(map[string]any)["text"])
	assert.Equal(t, "https://example.com/cb#access_token=%5Bredacted%5D&state=xyz", capture.Properties["callback"])
	assert.Equal(t, "GET https://example.com/x?token=%5Bredacted%5D failed: auth with [redacted] rejected", capture.Properties[propertyErrorMessage])
}

func TestSanitizeFreeText(t *testing.T) {
	for _, test := range []struct {
		name, value, want string
	}{
		{
			name:  "credential before pii",
			value: "use token phx_AAAA-415-555-0142-AAAAAAAAAAAAAA",
			want:  "use token [redacted]",
		},
		{
			name:  "longer token a phone pattern would cut",
			value: "Rotating phx_AAAAAAAA-415-555-0142-AAAAAAAAAAAAAAAAAAAA",
			want:  "Rotating [redacted]",
		},
		{
			name:  "posthog token and email",
			value: "Rotating token phc_123456789012345678901234567890 for user carol@example.org.",
			want:  "Rotating token [redacted] for user [redacted].",
		},
		{
			name:  "pii before url rewrite",
			value: "Open https://example.com/?email=alice@example.com&token=fakesecret",
			want:  "Open https://example.com/?email=%5Bredacted%5D&token=%5Bredacted%5D",
		},
		{
			name:  "vendor key and phone",
			value: "call +1 (415) 555-0142 with sk-proj-" + "T3BlbkFJabcd1234efgh5678ijkl9012mnop3456qrst7890wxyz",
			want:  "call [redacted] with [redacted]",
		},
		{
			name:  "binary gate before pii",
			value: strings.Repeat("AAAA/", 2052) + "4111111111111111/AAA",
			want:  binaryRedactedValue,
		},
		{
			name:  "prose stays",
			value: "Find setup instructions for v1.2.3 in /usr/local/lib/app.py",
			want:  "Find setup instructions for v1.2.3 in /usr/local/lib/app.py",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, sanitizeFreeText(test.value))
		})
	}
}

// Python's IPv6 and phone patterns backtrack until their lookarounds hold, and
// test every alternative against the original text in one pass.
func TestSanitizeFreeTextMatchesPythonLookaround(t *testing.T) {
	for _, test := range []struct {
		value, want string
	}{
		{"ping 2001:db8::1::1 now", "ping [redacted]::1 now"},
		{"dead::beef::1", "[redacted]::1"},
		{"listening on 2001:db8::1:8080x", "listening on [redacted]:8080x"},
		{"call +44 20 7946 0958x or +4111 1111 1111 1111country", "call [redacted] 0958x or [redacted] 1111country"},
	} {
		assert.Equal(t, test.want, sanitizeFreeText(test.value), test.value)
	}
}

func TestCaptureToolCallKeepsIDsInMetadata(t *testing.T) {
	id := "V1StGXR8_Z5jdHi6B-myT"
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName:      "query",
		DistinctID:    "user_1",
		Groups:        posthog.Groups{"organization": id},
		SetProperties: posthog.Properties{"workspace": id},
		Properties:    posthog.Properties{"request_id": id},
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, posthog.Groups{"organization": id}, capture.Groups)
	assert.Equal(t, posthog.Properties{"workspace": id}, capture.Properties[propertySet])
	assert.Equal(t, id, capture.Properties["request_id"])
	assert.Equal(t, redactedValue, sanitizeString(id), "the secret detector still covers payloads")
}

func TestCaptureToolCallSanitizesIntentAndToolName(t *testing.T) {
	client := &fakeEnqueueClient{}
	require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{
		ToolName: "https://svc:pw@internal.test/tools/search",
		Intent:   "use token phx_AAAA-415-555-0142-AAAAAAAAAAAAAA for alice@example.com",
	}))

	capture := requireCapture(t, client.messages[0])
	assert.Equal(t, "use token [redacted] for [redacted]", capture.Properties[propertyIntent])
	assert.Equal(t, "https://%5Bredacted%5D@internal.test/tools/search", capture.Properties[propertyToolName])
}

type resultContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type callToolResult struct {
	Content []resultContentBlock `json:"content"`
	IsError bool                 `json:"isError,omitempty"`
}

func TestCaptureToolCallTypedResponseWithLargeMedia(t *testing.T) {
	image := func(size int) resultContentBlock {
		return resultContentBlock{Type: "image", MIMEType: "image/png", Data: strings.Repeat("QUJD", size/4)}
	}
	text := func(value string) resultContentBlock { return resultContentBlock{Type: "text", Text: value} }
	redactedImage := map[string]any{"type": "text", "text": "[image content redacted - not supported by PostHog MCP analytics]"}

	tests := []struct {
		name     string
		response *callToolResult
		want     any
	}{
		{
			name:     "text kept and images redacted",
			response: &callToolResult{Content: []resultContentBlock{text("hello"), image(900_000), image(900_000)}},
			want: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "hello"}, redactedImage, redactedImage,
			}},
		},
		{
			name:     "images beyond the hard bound are omitted",
			response: &callToolResult{Content: []resultContentBlock{text("hello"), image(9 << 20)}},
			want:     oversizedPayloadValue,
		},
		{
			name:     "oversized text is omitted",
			response: &callToolResult{Content: []resultContentBlock{text(strings.Repeat("x", maxNormalizeBytes))}},
			want:     oversizedPayloadValue,
		},
		{
			name:     "redaction that still exceeds the cap is omitted",
			response: &callToolResult{Content: []resultContentBlock{text(strings.Repeat("x", maxNormalizeBytes)), image(900_000)}},
			want:     oversizedPayloadValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeEnqueueClient{}
			require.NoError(t, New(client).CaptureToolCall(context.Background(), ToolCall{ToolName: "query", Response: tt.response}))

			assert.Equal(t, tt.want, requireCapture(t, client.messages[0]).Properties[propertyResponse])
		})
	}
}
