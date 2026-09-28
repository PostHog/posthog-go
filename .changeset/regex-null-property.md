---
"posthog-go": patch
---

Fix local evaluation of `regex` against an explicit null property. The flags service stringifies JSON null as `"null"` before regex matching, and `not_regex` already did that. `regex` still used Go's `"<nil>"`, so a null property could match `^<nil>$` and miss `^null$`.
