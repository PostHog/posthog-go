package posthog

import (
	"sync"
)

// Version of the client.
const Version = "1.25.1"

// testVersionOverride pins the version this package's own tests assert against,
// so fixtures and header expectations survive a release bump. It is set from a
// _test.go file and is empty in every build that is not this package's tests.
//
// It replaces a flag.Lookup("test.v") probe, which was true in any test binary
// — including a user's — so applications reported posthog-go/1.0.0 while
// running their own tests and the resulting events were recorded against the
// wrong $lib_version.
var testVersionOverride string

var (
	cachedVersion     string
	cachedVersionOnce sync.Once
)

// getVersion returns the SDK version string, cached after the first call.
func getVersion() string {
	cachedVersionOnce.Do(func() {
		if testVersionOverride != "" {
			cachedVersion = testVersionOverride
			return
		}
		cachedVersion = Version
	})
	return cachedVersion
}
