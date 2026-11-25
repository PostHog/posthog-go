package posthogmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	posthog "github.com/posthog/posthog-go/v2"
)

var conversationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-7[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// normalizeConversationID keeps only a UUIDv7-shaped handle, lowercased, so
// the join key is bounded and identical across SDKs.
func normalizeConversationID(id string) string {
	if !conversationIDPattern.MatchString(id) {
		return ""
	}
	return strings.ToLower(id)
}

// deriveSessionID is "ses_" + H(x) + H(x + "::salt"), where H is two 32-bit
// FNV-1a lanes over runes, so every SDK derives the same session from the same
// input (runes equal posthog-js's UTF-16 code units in the basic multilingual
// plane, and posthog-python's code points always).
func deriveSessionID(x string) string {
	return "ses_" + sessionHash(x) + sessionHash(x+"::salt")
}

func sessionHash(x string) string {
	return fmt.Sprintf("%08x%08x", fnv1a(0x84222325, 0x1b3, x), fnv1a(0xcbf29ce4, 0x193, x))
}

func fnv1a(seed, prime uint32, x string) uint32 {
	lane := seed
	for _, char := range x {
		lane = (lane ^ uint32(char)) * prime
	}
	return lane
}

func resolveSession(call ToolCall) (sessionID string, source SessionSource, conversationID string) {
	conversationID = normalizeConversationID(call.ConversationID)
	switch {
	case call.SessionID != "" && call.SessionSource == SessionSourceHost:
		return call.SessionID, SessionSourceHost, conversationID
	case call.SessionID != "" && call.SessionSource == SessionSourceClientMetadata:
		return call.SessionID, SessionSourceClientMetadata, ""
	case conversationID != "":
		return deriveSessionID(conversationID), SessionSourceConversation, conversationID
	case call.SessionID != "":
		return call.SessionID, call.SessionSource, ""
	default:
		return mintSessionID(), SessionSourceGenerated, ""
	}
}

func (s SessionSource) valid() bool {
	switch s {
	case "", SessionSourceHost, SessionSourceClientMetadata, SessionSourceConversation, SessionSourceTransport, SessionSourceGenerated:
		return true
	}
	return false
}

// mintSessionID is a new "ses_" + UUIDv7, the session of a call that carries
// neither a transport session nor a conversation handle.
func mintSessionID() string {
	return "ses_" + uuid.Must(uuid.NewV7()).String()
}

// ClientConversation is what a tools/call request says about its client's
// conversation.
type ClientConversation struct {
	// SessionID is the $session_id derived from the client conversation id,
	// or "" when the request carries none.
	SessionID string
	// HandleFree reports a client that sends its conversation id on every
	// call, so it is advertised no conversation_id argument and minted no
	// handle.
	HandleFree bool
}

// ReadClientConversation reads the client conversation id of a tools/call
// request from its _meta, given the request's HTTP User-Agent and the client
// name from initialize or the request's own client info. Each key is read only
// from the client that defines it: ChatGPT's openai/session, Codex's
// sessionId, and VS Code's vscode.conversationId. Pass the SessionID as
// ToolCall.SessionID with SessionSourceClientMetadata. The value itself is
// never captured.
func ReadClientConversation(userAgent, clientName string, meta map[string]any) ClientConversation {
	for _, client := range conversationClients {
		if !client.recognizes(userAgent, clientName) {
			continue
		}
		conversation := ClientConversation{HandleFree: client.handleFree}
		value, _ := meta[client.key].(string)
		if id, ok := client.normalize(value); ok {
			conversation.SessionID = deriveSessionID(client.key + ":" + id)
		}
		return conversation
	}
	return ClientConversation{}
}

type conversationClient struct {
	key        string
	userAgent  *regexp.Regexp
	names      []string
	normalize  func(string) (string, bool)
	handleFree bool
}

var conversationClients = []conversationClient{
	{
		key:        "openai/session",
		userAgent:  regexp.MustCompile(`(?i)^openai-mcp/[^\s(]*\s*\(\s*chatgpt\s*[;,)]`),
		normalize:  printableASCII,
		handleFree: true,
	},
	{
		key:       "sessionId",
		userAgent: regexp.MustCompile(`(?i)^codex-mcp-client/`),
		names:     []string{"codex-mcp-client"},
		normalize: lowercaseUUID,
	},
	{
		key:       "vscode.conversationId",
		userAgent: regexp.MustCompile(`(?i)^visual studio code( - insiders)?/`),
		names:     []string{"Visual Studio Code", "Visual Studio Code - Insiders"},
		normalize: lowercaseUUID,
	},
}

func (c conversationClient) recognizes(userAgent, clientName string) bool {
	return c.userAgent.MatchString(userAgent) ||
		slices.ContainsFunc(c.names, func(name string) bool { return strings.EqualFold(name, clientName) })
}

func printableASCII(value string) (string, bool) {
	if len(value) < 1 || len(value) > 128 {
		return "", false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return "", false
		}
	}
	return value, true
}

func lowercaseUUID(value string) (string, bool) {
	value = strings.ToLower(value)
	return value, uuidPattern.MatchString(value) && value != "00000000-0000-0000-0000-000000000000"
}

// WithCallContext returns ctx carrying a tool call's session and identity as a
// posthog.RequestContext, so a custom event the call's handler captures with
// posthog.EnqueueWithContext carries the call's $session_id,
// $mcp_session_source, and distinct ID. Without a distinctID, the session is
// the distinct ID and no person profile is processed, as on the call's own
// events, unless ctx already carries a distinct ID.
func WithCallContext(ctx context.Context, sessionID string, source SessionSource, distinctID string) context.Context {
	scope := posthog.RequestContext{
		DistinctId: distinctID,
		SessionId:  sessionID,
		Properties: posthog.Properties{propertySessionID: sessionID, propertySessionSource: string(source)},
	}
	if outer, _ := posthog.RequestContextFromContext(ctx); distinctID == "" && outer.DistinctId == "" {
		scope.DistinctId = sessionID
		scope.Options = posthog.Options{optionProcessProfile: false}
	}
	return posthog.WithRequestContext(ctx, scope)
}

// SessionToken is a PostHog session token: a client's session, carried in the
// Mcp-Session-Id of a legacy initialize response and replayed by the client,
// as the unpadded base64url of {"sid":...,"cn":...,"cv":...,"pv":...}.
type SessionToken struct {
	SessionID       string
	ClientName      string
	ClientVersion   string
	ProtocolVersion string
}

const (
	maxSessionTokenLength   = 4096
	maxSessionTokenIDLength = 128
)

// ReadSessionToken decodes value as a PostHog session token. A value that is
// not one, such as a framework's own session id, is not a token.
func ReadSessionToken(value string) (SessionToken, bool) {
	if len(value) > maxSessionTokenLength {
		return SessionToken{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return SessionToken{}, false
	}
	var fields map[string]any
	if json.Unmarshal(decoded, &fields) != nil {
		return SessionToken{}, false
	}
	text := func(key string) string {
		value, _ := fields[key].(string)
		return value
	}
	id := text("sid")
	if id == "" || utf8.RuneCountInString(id) > maxSessionTokenIDLength {
		return SessionToken{}, false
	}
	return SessionToken{SessionID: id, ClientName: text("cn"), ClientVersion: text("cv"), ProtocolVersion: text("pv")}, true
}

// TransportSessionID is the $session_id of a transport session id: a PostHog
// session token's sid unchanged, else the id derived the way every PostHog MCP
// SDK derives it.
func TransportSessionID(id string) string {
	if token, ok := ReadSessionToken(id); ok {
		return token.SessionID
	}
	return deriveSessionID(id)
}

// Client is the MCP client that made a request.
type Client struct {
	Name            string
	Version         string
	ProtocolVersion string
}

// ResolveClient is the client a request identifies, from the first source
// that names it: the request's _meta clientInfo, then a replayed session
// token, then the initialize handshake, which the caller passes only when it
// is bound to the request's transport session. The protocol version comes from
// _meta, then the MCP-Protocol-Version header, then the token, then initialize.
func ResolveClient(meta map[string]any, protocolHeader string, token SessionToken, initialize Client) Client {
	info, _ := meta["io.modelcontextprotocol/clientInfo"].(map[string]any)
	name, _ := info["name"].(string)
	version, _ := info["version"].(string)
	protocol, _ := meta["io.modelcontextprotocol/protocolVersion"].(string)
	client := Client{Name: initialize.Name, Version: initialize.Version}
	switch {
	case name != "":
		client.Name, client.Version = name, version
	case token.ClientName != "":
		client.Name, client.Version = token.ClientName, token.ClientVersion
	}
	client.ProtocolVersion = firstNonEmpty(protocol, protocolHeader, token.ProtocolVersion, initialize.ProtocolVersion)
	return client
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
