---
"posthog-go": minor
---

Local flag evaluation honors experiment holdouts. A flag whose definition carries `filters.holdout` now resolves a held-out identifier to `holdout-<id>` before its release conditions, matching the server instead of silently bucketing the identifier into a regular variant.
