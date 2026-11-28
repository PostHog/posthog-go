---
"posthog-go": minor
---

Add an optional `GroupTypeIndex` field to `FlagProperty`. This is an additive public field. A `group` condition filter with a `group_type_index` now reads the properties and known-absent keys of that group type, as the flags service does, instead of those of the group its condition aggregates on. When that group's key isn't passed, the supplied properties for it count as complete, so a missing key is absent. When the index has no group type in the mapping, the filter doesn't match if the call passes any group context, and otherwise reads no properties.
