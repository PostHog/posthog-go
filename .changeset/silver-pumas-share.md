---
"posthog-go": minor
---

Add server-side request context helpers for net/http capture and exception events, plus `EvaluateFlagsWithContext` for using request-scoped distinct IDs during flag evaluation. Request-context flag evaluation does not generate personless IDs.
