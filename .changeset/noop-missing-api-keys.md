---
"posthog-go": patch
---

Return ErrSDKDisabled from no-op clients when the project API key is missing, return ErrNoPersonalAPIKey before making requests for Personal API key dependent methods when no Personal API key is configured, and return ErrNoDistinctID from EvaluateFlags when distinct_id is missing.
