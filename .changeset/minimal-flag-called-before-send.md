---
"posthog-go": major
---

Minimal `$feature_flag_called` events are trimmed to their fixed set of properties before `BeforeSend`, so the hook sees the trimmed set and the properties it adds are sent. In 1.x the trim ran after `BeforeSend` and removed them. See `docs/migration-v2.md`.
