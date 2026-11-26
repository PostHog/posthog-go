---
"posthog-go": patch
---

Local evaluation of a group-aggregated flag without a usable key for its group type now skips only the conditions on that group type, as the flags service does. A condition with `"aggregation_group_type_index": null` targets the person and is still evaluated, also when the flag is a dependency, so the flag can be `true`. With a group key, such a condition now reads person properties instead of the group's. A holdout that excludes anyone on a group flag without its group key is now inconclusive with `missing_context` and falls back to `/flags`, because the flags service would bucket it on an empty key.
