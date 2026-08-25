---
"posthog-go": minor
---

`FeatureFlagEvaluations.IsEnabled` now accepts an optional caller-supplied default value (`IsEnabled(key, defaultValue)`), returned when the flag has no value (not loaded, evaluation failed, or no flag with that key). A flag that has a value — including `false` and variant strings — always wins over the caller-supplied default. Calls with no default keep returning `false` for unknown flags, unchanged.
