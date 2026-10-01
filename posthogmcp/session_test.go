package posthogmcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
