package posthogmcpgo

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
)

const (
	sessionInactivityTimeout = 30 * time.Minute
	// maxGeneratedSessions bounds the sessions remembered at once, so a burst
	// of connections cannot grow the map before the timeout frees it.
	maxGeneratedSessions = 1024
)

// sessionResolver picks the $session_id of a tool call. A streamable HTTP
// session id becomes the id every PostHog MCP SDK derives from it. A stateless
// streamable HTTP request, which mcp-go gives an empty session id, gets a
// fresh id per call. Any other session (stdio, in-process, SSE) is a
// one-client connection: it gets a generated id that rotates after
// [sessionInactivityTimeout] without a tool call.
type sessionResolver struct {
	mu        sync.Mutex
	generated map[server.ClientSession]generatedSession
	lastSweep time.Time
}

type generatedSession struct {
	id           string
	lastActivity time.Time
}

func newSessionResolver() *sessionResolver {
	return &sessionResolver{generated: map[server.ClientSession]generatedSession{}}
}

func (r *sessionResolver) resolve(session server.ClientSession, now time.Time) string {
	if session == nil {
		return newGeneratedSessionID()
	}
	if _, streamable := session.(server.SessionWithStreamableHTTPConfig); streamable {
		if id := session.SessionID(); id != "" {
			return deterministicSessionID(id)
		}
		return newGeneratedSessionID()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweep(now)
	entry := r.generated[session]
	if entry.id == "" && len(r.generated) >= maxGeneratedSessions {
		r.evictOldest()
	}
	if entry.id == "" || now.Sub(entry.lastActivity) > sessionInactivityTimeout {
		entry.id = newGeneratedSessionID()
	}
	entry.lastActivity = now
	r.generated[session] = entry
	return entry.id
}

func newGeneratedSessionID() string {
	return "ses_" + uuid.Must(uuid.NewV7()).String()
}

// evictOldest drops the session with the least recent activity. It scans the
// map, which is fine because it only runs once the map is full.
func (r *sessionResolver) evictOldest() {
	var oldest server.ClientSession
	var oldestActivity time.Time
	for session, entry := range r.generated {
		if oldest == nil || entry.lastActivity.Before(oldestActivity) {
			oldest, oldestActivity = session, entry.lastActivity
		}
	}
	delete(r.generated, oldest)
}

// sweep drops sessions idle past the timeout, at most once per timeout, so the
// map cannot grow without bound and no goroutine is needed.
func (r *sessionResolver) sweep(now time.Time) {
	if now.Sub(r.lastSweep) <= sessionInactivityTimeout {
		return
	}
	r.lastSweep = now
	for session, entry := range r.generated {
		if now.Sub(entry.lastActivity) > sessionInactivityTimeout {
			delete(r.generated, session)
		}
	}
}

// deterministicSessionID is the cross-SDK derivation of a $session_id from a
// transport session id: "ses_" + H(input) + H(input + "::salt"), so servers
// in different languages group one MCP session together.
func deterministicSessionID(input string) string {
	return "ses_" + fnv1aHex(input) + fnv1aHex(input+"::salt")
}

// fnv1aHex is two 32-bit FNV-1a lanes with different seeds and primes, each
// as eight lowercase hexadecimal digits. It is not cryptographic.
func fnv1aHex(input string) string {
	lane1, lane2 := uint32(0x84222325), uint32(0xcbf29ce4)
	for _, char := range input {
		lane1 = (lane1 ^ uint32(char)) * 0x000001b3
		lane2 = (lane2 ^ uint32(char)) * 0x00000193
	}
	return fmt.Sprintf("%08x%08x", lane1, lane2)
}

// protocolVersions are the protocol versions negotiated by initialize on
// one-client connections (stdio, in-process, SSE), until the session ends.
// Streamable HTTP sends the version in a header on every request instead.
type protocolVersions struct {
	mu       sync.Mutex
	versions map[server.ClientSession]string
}

func newProtocolVersions() *protocolVersions {
	return &protocolVersions{versions: map[server.ClientSession]string{}}
}

func (p *protocolVersions) record(session server.ClientSession, version string) {
	if _, streamable := session.(server.SessionWithStreamableHTTPConfig); streamable || session == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.versions[session] = version
}

func (p *protocolVersions) of(session server.ClientSession) string {
	if session == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.versions[session]
}

func (p *protocolVersions) forget(session server.ClientSession) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.versions, session)
}
