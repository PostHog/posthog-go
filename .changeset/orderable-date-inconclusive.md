---
"posthog-go": patch
---

Return `InconclusiveMatchError` for non-orderable `gt`/`lt`/`gte`/`lte` operands and invalid date comparisons during local flag evaluation, so later condition groups can still match instead of aborting evaluation.
