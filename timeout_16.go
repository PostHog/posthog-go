//go:build go1.6
// +build go1.6

package posthog

import "net/http"

// http clients on versions of go after 1.6 always support timeout.
func supportsTimeout(_ http.RoundTripper) bool {
	return true
}
