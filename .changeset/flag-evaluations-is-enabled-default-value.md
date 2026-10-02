---
'posthog-go': minor
---

Add an optional `defaultValue` argument to `FeatureFlagEvaluations.IsEnabled`. It is returned when the flag has no value in the snapshot — no flag with that key was evaluated, or the snapshot is nil because flags were never loaded or the request failed — while a flag that does have a value, including a disabled flag and a multivariate variant, still wins over the default. Calls that omit the argument keep returning `false` for an unknown flag.
