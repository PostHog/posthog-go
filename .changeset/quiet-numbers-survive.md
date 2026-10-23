---
"posthog-go": patch
---

Preserve captured events with non-finite numeric properties by serializing NaN and infinities as null in both capture protocols. Arbitrary structs containing these numbers are replaced with null as a whole property.
