---
"posthog-go": patch
---

Capture with `SendFeatureFlags(true)` now prefers local evaluation when flag definitions are loaded, falling back to a remote `/flags` request only for flags that can't be computed locally. `OnlyEvaluateLocally` remains strictly local with no remote fallback.

Captured events now exclude flags that evaluate to `false` from `$active_feature_flags` (they are still attached as `$feature/<key>=false`), matching the other PostHog SDKs.
