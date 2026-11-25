package posthogmcpgo

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

const (
	sessionInactivityTimeout = 30 * time.Minute
	// maxGeneratedSessions bounds the sessions remembered at once, so a burst
	// of connections cannot grow the map before the timeout frees it.
	maxGeneratedSessions = 1024
)

// sessionResolver picks the $session_id of a tool call. A transport session id
// (streamable HTTP or SSE) becomes the id every PostHog MCP SDK derives from
// it. A stateless streamable HTTP request, which mcp-go gives an empty session
// id, gets a fresh id per call. A stdio or in-process session is a one-client
// connection: it gets a generated id that rotates after
// [sessionInactivityTimeout] without a tool call.
type sessionResolver struct {
	mu        sync.Mutex
	generated map[server.ClientSession]generatedSession
	lastSweep time.Time
}

type generatedSession struct {
	id           string
	lastActivity time.Time
	// running counts the calls in flight, during which the session never
	// expires: the timeout counts from when the last call ended.
	running int
}

func newSessionResolver() *sessionResolver {
	return &sessionResolver{generated: map[server.ClientSession]generatedSession{}}
}

func (r *sessionResolver) resolve(session server.ClientSession, now time.Time) callSession {
	if session == nil {
		return callSession{id: newGeneratedSessionID(), source: posthogmcp.SessionSourceGenerated}
	}
	if !oneClient(session) {
		if id := session.SessionID(); id != "" {
			return callSession{id: posthogmcp.TransportSessionID(id), source: posthogmcp.SessionSourceTransport}
		}
		return callSession{id: newGeneratedSessionID(), source: posthogmcp.SessionSourceGenerated}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweep(now)
	entry := r.generated[session]
	if entry.id == "" && len(r.generated) >= maxGeneratedSessions {
		r.evictOldest()
	}
	if entry.id == "" || entry.running == 0 && now.Sub(entry.lastActivity) >= sessionInactivityTimeout {
		entry.id = newGeneratedSessionID()
	}
	entry.lastActivity = now
	entry.running++
	r.generated[session] = entry
	return callSession{id: entry.id, source: posthogmcp.SessionSourceGenerated}
}

// stdioSessionType is mcp-go's unexported stdio session, which reports the
// session id "stdio" for any process.
const stdioSessionType = "*server.stdioSession"

// carriesSession reports whether session groups a client's calls: a one-client
// connection, or one with a transport session id. A stateless streamable HTTP
// request has neither.
func carriesSession(session server.ClientSession) bool {
	return session != nil && (oneClient(session) || session.SessionID() != "")
}

// oneClient is whether a session is a connection with a single client, whose
// transport has no session id of its own to derive from.
func oneClient(session server.ClientSession) bool {
	if _, inProcess := session.(*server.InProcessSession); inProcess {
		return true
	}
	return fmt.Sprintf("%T", session) == stdioSessionType
}

// forget drops a closed session's generated id, so the map holds only open
// sessions.
func (r *sessionResolver) forget(session server.ClientSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.generated, session)
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
		if entry.running == 0 && now.Sub(entry.lastActivity) >= sessionInactivityTimeout {
			delete(r.generated, session)
		}
	}
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

// finish records the end of a call that resolve started on a one-client
// session.
func (r *sessionResolver) finish(session server.ClientSession, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.generated[session]; ok {
		entry.lastActivity = now
		entry.running = max(entry.running-1, 0)
		r.generated[session] = entry
	}
}
