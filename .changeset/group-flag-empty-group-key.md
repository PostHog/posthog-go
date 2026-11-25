---
"posthog-go": patch
---

Local evaluation now treats an empty group key in `Groups` as no group key, as the flags service does. A group-aggregated flag, condition, or flag dependency on that group type no longer matches or buckets on the empty key, so the flag resolves `false` locally without a `/flags` fallback.
