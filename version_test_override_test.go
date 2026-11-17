package posthog

// The suite's fixtures and header assertions are written against a fixed
// version so a release bump does not rewrite them.
func init() { testVersionOverride = "1.0.0" }
