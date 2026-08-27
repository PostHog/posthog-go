package posthog

import (
	"os"
	"strings"
	"sync"
)

// releaseIDEnvVar is the environment variable the SDK reads the release id from.
//
// This is the native, deploy-time counterpart to injecting $release_id into a web bundle. A
// compiled binary has no bundle, so a build tool creates the release with `posthog-cli release
// resolve`, launches the app with the printed id in this variable, and the SDK reports it as
// $release_id on every exception — so the server resolves the exception's release by a direct id
// lookup, with no release name or version having to match anything the app reports.
const releaseIDEnvVar = "POSTHOG_RELEASE_ID"

var (
	releaseIDOnce  sync.Once
	releaseIDValue *string
)

// releaseIDFromEnv returns the release id from POSTHOG_RELEASE_ID, read once. It returns nil when
// the variable is unset or blank, so no $release_id is sent.
func releaseIDFromEnv() *string {
	releaseIDOnce.Do(func() {
		releaseIDValue = normalizeReleaseID(os.Getenv(releaseIDEnvVar))
	})
	return releaseIDValue
}

// normalizeReleaseID trims the raw value and treats a blank string as unset, so POSTHOG_RELEASE_ID=
// (or whitespace) does not send an empty $release_id.
func normalizeReleaseID(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
