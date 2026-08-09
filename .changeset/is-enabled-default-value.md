---
"posthog-go": minor
---

`FeatureFlagEvaluations.IsEnabled` accepts an optional caller-supplied default value, returned whenever the flag has no value (missing key, flags not loaded, or a failed `/flags` request). A flag with a real value, including `false` or a variant, always wins over the default. Existing calls without a default are unaffected and continue to fall back to `false` on a miss.
