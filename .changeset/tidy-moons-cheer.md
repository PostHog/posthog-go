---
"posthog-go": minor
---

Add `FeatureFlagEvaluations.IsEnabledOrDefault(key, defaultValue)`, which returns the caller-supplied default when a flag has no value for the user instead of collapsing "missing" to `false`. `IsEnabled` is unchanged.
