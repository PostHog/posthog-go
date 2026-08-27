---
"posthog-go": patch
---

Align local feature flag property matching with the flags service: use ASCII-only case folding for contains, prefix, and suffix operators, and Unicode lowercasing rather than broader case folding for `exact` and `is_not`.
