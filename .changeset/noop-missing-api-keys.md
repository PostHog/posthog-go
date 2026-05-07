---
"posthog-go": patch
---

No-op SDK calls when the project API key is missing, return ErrNoPersonalAPIKey before making requests for Personal API key dependent methods when no Personal API key is configured, and return ErrNoDistinctID from public feature flag APIs when distinct_id is missing.
