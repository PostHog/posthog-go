---
"posthog-go": patch
---

Feature flag payloads that aren't valid JSON now read as no payload instead of being returned as a raw string. `GetFeatureFlagPayload`, `GetFeatureFlagResult().RawPayload` and `EvaluateFlags(...).GetFlagPayload` return the empty payload and log a warning when a flag's stored payload can't be parsed, so callers no longer receive a string that `json.Unmarshal` (or `GetPayloadAs`) is guaranteed to reject. The flag's value and other flags' payloads are unaffected.
