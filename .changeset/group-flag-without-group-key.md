---
"posthog-go": patch
---

Local evaluation now skips a group-aggregated flag's conditions on its group type when the call doesn't pass a key for that group type, as the flags service does, so a flag with only such conditions resolves `false`. It used to be inconclusive and fall back to `/flags`, or return an error with `OnlyEvaluateLocally`. A group key passed without the group properties a condition needs is still inconclusive and can still fall back to `/flags`. A flag with experience continuity is still unresolved.
