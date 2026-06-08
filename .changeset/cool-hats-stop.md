---
"posthog-go": minor
---

Support the `early_exit` flag filter in local evaluation. When a flag's `filters.early_exit` is `true` and a condition group's property filters match (or there are none) but the rollout percentage excludes the user, evaluation now stops and returns `false` immediately instead of falling through to later groups. Mirrors the server-side (Rust) evaluation engine. A property-filter mismatch still falls through as before, and behaviour is unchanged when `early_exit` is unset or `false`.
