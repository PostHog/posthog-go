---
"posthog-go": patch
---

String flag operators treat an explicit null property as `"null"` like regex does. `json.Number` property values work in numeric comparisons. Flag dependency evaluation forwards the caller's groups and group properties into nested local evaluation.
