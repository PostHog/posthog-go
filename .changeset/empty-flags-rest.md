---
"posthog-go": patch
---

Return an empty feature flag snapshot without evaluating flags when `FlagKeys` is an explicit empty slice.
