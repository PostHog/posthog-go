---
"posthog-go": patch
---

Local evaluation now reads a `person` filter in a group-aggregated condition, at the top of the condition or in a cohort, from the person properties and person known-absent keys, as the flags service does. It used to read the group's properties, so a person filter could match on a group property of the same name.
