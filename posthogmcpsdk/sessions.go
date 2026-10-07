package posthogmcpsdk

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
	generated map[*mcpsdk.ServerSession]generatedSession
	lastSweep time.Time
}

type generatedSession struct {
	id           string
	lastActivity time.Time
}

func newSessionResolver(now func() time.Time) *sessionResolver {
	return &sessionResolver{now: now, generated: map[*mcpsdk.ServerSession]generatedSession{}}
}

func (r *sessionResolver) resolve(session *mcpsdk.ServerSession, requestScoped bool) string {
	if requestScoped {
		return newGeneratedSessionID()
	}
	if session != nil && session.ID() != "" {
		return deterministicSessionID(session.ID())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
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

// evictOldest drops the session with the least recent activity. It scans the
// map, which is fine because it only runs once the map is full.
func (r *sessionResolver) evictOldest() {
	var oldest *mcpsdk.ServerSession
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
