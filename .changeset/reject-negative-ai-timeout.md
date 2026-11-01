---
"posthog-go": patch
---

`Config.Validate` rejects a negative `CaptureAIBatchUploadTimeout`. Before, it passed and meant AI uploads had no timeout. Only zero selects the default.
