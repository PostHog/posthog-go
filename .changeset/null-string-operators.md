---
"posthog-go": patch
---

Fix local feature flag string and regex operators to stringify explicit null values as "null", matching the flags service. Flag results and variant selection can change for explicit-null properties; missing properties remain inconclusive and eligible for remote evaluation.
