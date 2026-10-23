---
"posthog-go": patch
---

Fall back to remote feature flag evaluation for regex syntax and character-class semantics that cannot be evaluated consistently with the flags service, including through negated cohorts.
