---
"posthog-go": patch
---

Local evaluation now evaluates a flag dependency in its own context, as the flags service does. A dependency on a flag with a different aggregation used to fall back to `/flags`. It now resolves locally with its own aggregation, properties, known-absent keys and bucketing identifier (the group key for a group-aggregated flag). A group-aggregated dependency whose group isn't passed is evaluated as it would be directly. A dependency chain deeper than 100 flags is inconclusive. A `group` filter in a cohort with an omitted or null `group_type_index` now uses the known-absent keys of the group type its condition aggregates on.
