---
"posthog-go": patch
---

Honor the definitions snapshot's `property_matching_version` during local flag evaluation: version 2 uses explicit boolean equality, while missing/1 and unknown versions retain legacy matching. Keep definitions and matching semantics together across reloads, cached 304 responses, group/cohort rules, and flag dependencies. Also preserve JSON-decoded dependency chains in cohort leaves, while requiring server evaluation for dependencies that need group context or are reached from group-targeted conditions.
