package posthogmcp

import (
	"context"
	"strings"
	"testing"

	posthog "github.com/posthog/posthog-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Expected values come from posthog-js's deterministicPrefixedId.
func TestDeriveSessionIDMatchesReference(t *testing.T) {
	for input, want := range map[string]string{
		"0190f0e8-7a6b-7c3d-9e4f-5a6b7c8d9e0f": "ses_6df45f0102a182bcd5e8dd5dad6c65a0",
		"session-123":                          "ses_346bdc9a6b5cb06913bb476a65021eb5",
		"":                                     "ses_84222325cbf29ce4805967715e2fab98",
		"é-ünï":                                "ses_f52aeeca2d955f05d88a791a4ead6a71",
	} {
		assert.Equal(t, want, deriveSessionID(input), "input %q", input)
	}
}

func TestReadClientConversation(t *testing.T) {
	const (
		chatGPT   = "openai-mcp/1.0.0 (ChatGPT)"
		codex     = "codex-mcp-client/0.50.0"
		vsCode    = "Visual Studio Code/1.105.0"
		chatGPTID = "ses_2f5d35b7108d17c807766f2f769a1e8c"
		codexID   = "ses_0fdf126b8e2c1f9689ca12f3dd3383e6"
		vsCodeID  = "ses_0571acb894e6753d33e37e5c2b327c59"
		codexUUID = "019a4f2e-1c3d-7b8a-9e0f-1a2b3c4d5e6f"
	)
	for _, test := range []struct {
		name       string
		userAgent  string
		clientName string
		meta       map[string]any
		want       ClientConversation
	}{
		{name: "chatgpt session", userAgent: chatGPT, meta: map[string]any{"openai/session": "v1/conv-example-0001"}, want: ClientConversation{SessionID: chatGPTID, HandleFree: true}},
		{name: "chatgpt user agent with a platform", userAgent: "openai-mcp/1.0.0 ( chatgpt; web)", meta: map[string]any{"openai/session": "v1/conv-example-0001"}, want: ClientConversation{SessionID: chatGPTID, HandleFree: true}},
		{name: "chatgpt user agent in another case", userAgent: "OPENAI-MCP/1.0.0 (CHATGPT)", meta: map[string]any{"openai/session": "v1/conv-example-0001"}, want: ClientConversation{SessionID: chatGPTID, HandleFree: true}},
		{name: "chatgpt without its id is still handle-free", userAgent: chatGPT, want: ClientConversation{HandleFree: true}},
		{name: "chatgpt empty id", userAgent: chatGPT, meta: map[string]any{"openai/session": ""}, want: ClientConversation{HandleFree: true}},
		{name: "chatgpt 128 characters", userAgent: chatGPT, meta: map[string]any{"openai/session": strings.Repeat("a", 128)}, want: ClientConversation{SessionID: deriveSessionID("openai/session:" + strings.Repeat("a", 128)), HandleFree: true}},
		{name: "chatgpt 129 characters", userAgent: chatGPT, meta: map[string]any{"openai/session": strings.Repeat("a", 129)}, want: ClientConversation{HandleFree: true}},
		{name: "chatgpt non-printable", userAgent: chatGPT, meta: map[string]any{"openai/session": "conv\n1"}, want: ClientConversation{HandleFree: true}},
		{name: "chatgpt non-ascii", userAgent: chatGPT, meta: map[string]any{"openai/session": "conv-é"}, want: ClientConversation{HandleFree: true}},
		{name: "chatgpt non-string", userAgent: chatGPT, meta: map[string]any{"openai/session": 42}, want: ClientConversation{HandleFree: true}},
		{name: "codex agent of openai-mcp", userAgent: "openai-mcp/1.0.0 (Codex)", meta: map[string]any{"openai/session": "v1/conv-example-0001"}},
		{name: "chatgpt agent", userAgent: "openai-mcp/1.0.0 (ChatGPT-Agent)", meta: map[string]any{"openai/session": "v1/conv-example-0001"}},
		{name: "chatgpt not at the start", userAgent: "Mozilla/5.0 openai-mcp/1.0.0 (ChatGPT)", meta: map[string]any{"openai/session": "v1/conv-example-0001"}},
		{name: "chatgpt client name alone", clientName: "ChatGPT", meta: map[string]any{"openai/session": "v1/conv-example-0001"}},
		{name: "codex user agent", userAgent: codex, meta: map[string]any{"sessionId": "019A4F2E-1C3D-7B8A-9E0F-1A2B3C4D5E6F"}, want: ClientConversation{SessionID: codexID}},
		{name: "codex client name", clientName: "Codex-MCP-Client", meta: map[string]any{"sessionId": codexUUID}, want: ClientConversation{SessionID: codexID}},
		{name: "codex invalid id", userAgent: codex, meta: map[string]any{"sessionId": "not-a-uuid"}},
		{name: "codex nil uuid", userAgent: codex, meta: map[string]any{"sessionId": "00000000-0000-0000-0000-000000000000"}},
		{name: "codex braced uuid", userAgent: codex, meta: map[string]any{"sessionId": "{" + codexUUID + "}"}},
		{name: "codex reads only its own key", userAgent: codex, meta: map[string]any{"vscode.conversationId": codexUUID}},
		{name: "vs code client name", clientName: "Visual Studio Code", meta: map[string]any{"vscode.conversationId": "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e"}, want: ClientConversation{SessionID: vsCodeID}},
		{name: "vs code user agent", userAgent: vsCode, meta: map[string]any{"vscode.conversationId": "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e"}, want: ClientConversation{SessionID: vsCodeID}},
		{name: "vs code insiders", clientName: "visual studio code - insiders", meta: map[string]any{"vscode.conversationId": "3f1c2b7e-8d4a-4c6b-9a1e-5f2d7c8b9a0e"}, want: ClientConversation{SessionID: vsCodeID}},
		{name: "an unknown client's keys are not read", userAgent: "example-client/1.0", meta: map[string]any{"sessionId": codexUUID, "openai/session": "v1/conv-example-0001"}},
		{name: "the first recognizing row decides", userAgent: chatGPT, clientName: "codex-mcp-client", meta: map[string]any{"sessionId": codexUUID}, want: ClientConversation{HandleFree: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ReadClientConversation(test.userAgent, test.clientName, test.meta))
		})
	}
}

func TestWithCallContext(t *testing.T) {
	for _, test := range []struct {
		name       string
		host       *posthog.RequestContext
		distinctID string
		want       posthog.RequestContext
	}{
		{
			name:       "an identified call",
			distinctID: "user_1",
			want:       posthog.RequestContext{DistinctId: "user_1", SessionId: "s1", Properties: posthog.Properties{"$session_id": "s1", "$mcp_session_source": "client_metadata"}},
		},
		{
			name: "an anonymous call is its session, without a person",
			want: posthog.RequestContext{DistinctId: "s1", SessionId: "s1", Properties: posthog.Properties{"$session_id": "s1", "$mcp_session_source": "client_metadata"}, Options: posthog.Options{"process_person_profile": false}},
		},
		{
			name: "the call's session replaces a host's $session_id property",
			host: &posthog.RequestContext{DistinctId: "host_user", Properties: posthog.Properties{"$session_id": "host_session"}},
			want: posthog.RequestContext{DistinctId: "host_user", SessionId: "s1", Properties: posthog.Properties{"$session_id": "s1", "$mcp_session_source": "client_metadata"}},
		},
		{
			name: "an anonymous call keeps the host's distinct id and its session",
			host: &posthog.RequestContext{DistinctId: "host_user", SessionId: "host_session"},
			want: posthog.RequestContext{DistinctId: "host_user", SessionId: "s1", Properties: posthog.Properties{"$session_id": "s1", "$mcp_session_source": "client_metadata"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.host != nil {
				ctx = posthog.WithRequestContext(ctx, *test.host)
			}
			got, ok := posthog.RequestContextFromContext(WithCallContext(ctx, "s1", SessionSourceClientMetadata, test.distinctID))
			require.True(t, ok)
			assert.Equal(t, test.want.DistinctId, got.DistinctId)
			assert.Equal(t, test.want.SessionId, got.SessionId)
			assert.Equal(t, test.want.Properties, got.Properties)
			assert.Equal(t, test.want.Options, got.Options)
		})
	}
}
