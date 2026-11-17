---
"posthog-go": minor
---

Add `ReloadFeatureFlagsWithContext`, which fetches local feature flag definitions and returns once they are in use, or with the error that kept them from loading. Reach it through the new `FlagReloader` interface (`client.(posthog.FlagReloader)`), so implementations and mocks of `Client` are unaffected. `ReloadFeatureFlags` still only schedules the fetch.
