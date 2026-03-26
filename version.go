package posthog

import (
	"flag"
	"sync"
)

// Version of the client.
const Version = "1.11.1"

var (
	cachedVersion     string
	cachedVersionOnce sync.Once
)

// getVersion returns the SDK version string. The result is cached after first call
// to avoid repeated flag.Lookup on every event.
func getVersion() string {
	cachedVersionOnce.Do(func() {
		if flag.Lookup("test.v") != nil {
			cachedVersion = "1.0.0"
		} else {
			cachedVersion = Version
		}
	})
	return cachedVersion
}
