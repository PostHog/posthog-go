---
"posthog-go": patch
---

Fix a panic in local evaluation of a group flag when the group key is a number, such as `Groups{"company": 42}`. `Groups` values are free-form, but local evaluation asserted the key was a `string`, so `GetFeatureFlag`, `GetAllFlags` and `Enqueue` of a `Capture` with `SendFeatureFlags` panicked on the caller's goroutine. A mixed-targeting condition silently skipped a numeric key instead. A numeric key is now bucketed by its JSON form (`42` as `"42"`), as the flags service does. Other non-string keys fall back to the API.
