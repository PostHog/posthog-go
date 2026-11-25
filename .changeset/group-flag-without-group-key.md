---
"posthog-go": patch
---

Local evaluation now resolves a group-aggregated flag as `false` when the call doesn't pass a key for its group type, as the flags service does. It used to be inconclusive and fall back to `/flags`, or return an error with `OnlyEvaluateLocally`. A group key passed without the group properties a condition needs is still inconclusive and can still fall back to `/flags`. A flag with experience continuity is still unresolved.
