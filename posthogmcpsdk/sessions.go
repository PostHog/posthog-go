package posthogmcpsdk

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthog/posthog-go/v2/posthogmcp"
)

const (
	sessionInactivityTimeout = 30 * time.Minute
	// maxGeneratedSessions bounds the sessions remembered at once, so a burst
	// of connections cannot grow the map before the timeout frees it.
	maxGeneratedSessions = 1024
)

// sessionResolver picks the $session_id of a tool call: the transport session
// id derived into the id every PostHog MCP SDK computes for it, or, without
// one (stdio and in-memory transports), an SDK-generated id per go-sdk session
// that rotates after [sessionInactivityTimeout] without activity. A
// request-scoped session, which a stateless HTTP server creates per request,
// is never remembered: it gets a fresh id on each call, which keeps the many
// clients of one stateless HTTP server apart at the cost of fragmenting a
// conversation across requests.
type sessionResolver struct {
	now func() time.Time

	mu        sync.Mutex
	generated map[*mcpsdk.ServerSession]*generatedSession
	lastSweep time.Time
}

type generatedSession struct {
	id           string
	lastActivity time.Time
	// running counts the calls in flight, during which the session never
	// expires: the timeout counts from when the last call ended.
	running int
}

func newSessionResolver(now func() time.Time) *sessionResolver {
	return &sessionResolver{now: now, generated: map[*mcpsdk.ServerSession]*generatedSession{}}
}

// resolve returns the call's session and a release func to call once the call
// ends. Only a one-client session counts its calls in flight; release is a
// no-op for the others. It ends the very entry resolve counted the call on, so
// an entry evicted mid-call never lends its count to a later one.
func (r *sessionResolver) resolve(session *mcpsdk.ServerSession, requestScoped bool) (callSession, func()) {
	if requestScoped {
		return callSession{id: newGeneratedSessionID(), source: posthogmcp.SessionSourceGenerated}, func() {}
	}
	if session != nil && session.ID() != "" {
		return callSession{id: deterministicSessionID(session.ID()), source: posthogmcp.SessionSourceTransport}, func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweep(now)
	entry := r.generated[session]
	if entry == nil {
		if len(r.generated) >= maxGeneratedSessions {
			r.evictOldest()
		}
		entry = &generatedSession{}
		r.generated[session] = entry
	}
	if entry.id == "" || entry.running == 0 && r.idleSince(entry, now) {
		entry.id = newGeneratedSessionID()
	}
	entry.lastActivity = now
	entry.running++
	return callSession{id: entry.id, source: posthogmcp.SessionSourceGenerated}, func() { r.release(entry) }
}

// idleSince reports whether the session's last call ended the timeout ago or
// more, which the spec says starts a new session.
func (r *sessionResolver) idleSince(entry *generatedSession, now time.Time) bool {
	return now.Sub(entry.lastActivity) >= sessionInactivityTimeout
}

// carriesSession reports whether req arrived on a transport session: a
// one-client connection such as stdio or in-memory, which go-sdk serves
// without RequestExtra, or an HTTP request that sends Mcp-Session-Id. A
// stateless go-sdk server gives a request without that header a random
// session id of its own, which groups nothing.
func carriesSession(req *mcpsdk.CallToolRequest) bool {
	return req.Extra == nil || req.Extra.Header.Get("Mcp-Session-Id") != ""
}

func newGeneratedSessionID() string {
	return "ses_" + uuid.Must(uuid.NewV7()).String()
}

// evictOldest drops the idle session with the least recent activity, or the
// oldest one when every session has a call in flight. It scans the map, which
// is fine because it only runs once the map is full.
func (r *sessionResolver) evictOldest() {
	var oldest *mcpsdk.ServerSession
	var oldestEntry *generatedSession
	for session, entry := range r.generated {
		if oldest == nil || entry.evictsBefore(oldestEntry) {
			oldest, oldestEntry = session, entry
		}
	}
	delete(r.generated, oldest)
}

func (e *generatedSession) evictsBefore(other *generatedSession) bool {
	if (e.running > 0) != (other.running > 0) {
		return e.running == 0
	}
	return e.lastActivity.Before(other.lastActivity)
}

// sweep drops sessions idle past the timeout, at most once per timeout, so the
// map cannot grow without bound and no goroutine is needed.
func (r *sessionResolver) sweep(now time.Time) {
	if now.Sub(r.lastSweep) < sessionInactivityTimeout {
		return
	}
	r.lastSweep = now
	for session, entry := range r.generated {
		if entry.running == 0 && r.idleSince(entry, now) {
			delete(r.generated, session)
		}
	}
}

// deterministicSessionID is the cross-SDK derivation of a $session_id from a
// transport session id: "ses_" + H(input) + H(input + "::salt"), so servers
// in different languages group one MCP session together. It hashes runes,
// which equals posthog-js's UTF-16 code units for every character in the
// basic multilingual plane and posthog-python's code points always.
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

// release records the end of a call that resolve counted on entry.
func (r *sessionResolver) release(entry *generatedSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.lastActivity = r.now()
	entry.running = max(entry.running-1, 0)
}
