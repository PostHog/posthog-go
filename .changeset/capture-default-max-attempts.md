---
"posthog-go": minor
---

Change the default capture delivery budget from 10 attempts to 4 (`DefaultMaxAttempts`) when `Config.MaxRetries` is unset, aligning with the cross-SDK Capture V1 parity standard (posthog-rs uses the same envelope). This affects **both** the v0 (`/batch/`) and v1 send paths, since they share the attempt budget. Callers that set `MaxRetries` explicitly are unaffected.
