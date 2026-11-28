---
"posthog-go": patch
---

A `$feature_enrollment/<flag key>` person property listed in `PersonPropertyKeysKnownAbsent` now means the person isn't enrolled, even when a value is also passed, so the flag's holdout and release conditions decide it, as the flags service does.
