package posthogmcp

import (
	"fmt"
	"regexp"
	"strings"
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
// FNV-1a lanes, so every SDK derives the same session from the same input.
func deriveSessionID(x string) string {
	return "ses_" + sessionHash(x) + sessionHash(x+"::salt")
}

func sessionHash(x string) string {
	return fmt.Sprintf("%08x%08x", fnv1a(0x84222325, 0x1b3, x), fnv1a(0xcbf29ce4, 0x193, x))
}

func fnv1a(seed, prime uint32, x string) uint32 {
	lane := seed
	for i := 0; i < len(x); i++ {
		lane = (lane ^ uint32(x[i])) * prime
	}
	return lane
}
