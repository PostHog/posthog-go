---
"posthog-go": patch
---

Report per-event capture results for event uuids sent in a non-canonical form, such as uppercase or without hyphens. A caller-supplied `Uuid` is now normalized to the lowercase hyphenated form. Previously a `drop` or `retry` result for such an event was silently ignored, because the capture endpoint keys its results by the canonical form.
