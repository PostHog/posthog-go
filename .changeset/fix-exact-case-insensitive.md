---
"posthog-go": patch
---

Fix `exact`/`is_not` local flag evaluation to match case-insensitively and after string coercion, consistent with the other string operators here and the reference SDKs. A plain Go `==` made them case- and type-sensitive, so e.g. `exact "US"` did not match a `"us"` property value and `exact 1` did not match a `"1"` property value.
