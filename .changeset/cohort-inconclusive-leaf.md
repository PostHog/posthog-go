---
"posthog-go": patch
---

Fix local cohort matching deciding a flag from a property the caller didn't pass. A cohort entry that can't be evaluated locally (for example, a person property missing from `PersonProperties`) was treated as a non-match. In an `AND` group that made the whole cohort a definite non-match, and with `negation` in an `OR` group it made the cohort a match. Such an entry is now inconclusive, as in the Python and Node SDKs and the sdk-specs local evaluator spec, so the flag falls back to the API unless another entry decides the group.
