---
"posthog-go": patch
---

Fix local evaluation of a device-bucketed flag when no device id is provided. Person conditions were hashed with the distinct id, so the flag could come back enabled or disabled for the wrong bucket. Those conditions are now inconclusive and can fall back to `/flags`. An empty device id is treated as missing. Group-level aggregation still hashes the group key.
