---
"posthog-go": patch
---

Cohort groups no longer send a flag to /flags when a later sibling already decides the group, and a /flags result with `failed: true` is no longer reported as a successful disable.
