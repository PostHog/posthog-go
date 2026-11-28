---
"posthog-go": patch
---

Local evaluation now treats an empty group key in `Groups` as no group key, as the flags service does. Conditions on that group type, in a flag or a flag dependency, are skipped instead of matching or bucketing on the empty key, without a `/flags` fallback.
