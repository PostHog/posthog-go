package posthogmcpsdk

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const sessionInactivityTimeout = 30 * time.Minute

// sessionResolver picks the $session_id of a tool call: the transport session
// id derived into the id every PostHog MCP SDK computes for it, or, without
// one (stdio and in-memory transports, stateless HTTP), an SDK-generated id
// per go-sdk session that rotates after [sessionInactivityTimeout] without
// activity. Keying by session keeps the many clients of one stateless HTTP
// server apart, at the cost of fragmenting a conversation across requests.
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

func (r *sessionResolver) resolve(session *mcpsdk.ServerSession) string {
	if session != nil && session.ID() != "" {
		return deterministicSessionID(session.ID())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweep(now)
	entry := r.generated[session]
	if entry.id == "" || now.Sub(entry.lastActivity) > sessionInactivityTimeout {
		entry.id = "ses_" + uuid.Must(uuid.NewV7()).String()
	}
	entry.lastActivity = now
	r.generated[session] = entry
	return entry.id
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
