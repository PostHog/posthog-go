---
"posthog-go": patch
---

Fix `not_regex` local flag evaluation erroring on non-string/int property values. The `not_regex` operator used a manual string/int type switch and returned an error for other types, most notably `float64`, which is what JSON numbers deserialize to, so it failed on a numeric property value even though `regex` handled it. `not_regex` now coerces both sides with `valueToString`, mirroring `regex`. It also no longer coerces a `nil` property value to the Go-specific `<nil>` string; like the server, a null property now yields no-match for `not_regex`.
