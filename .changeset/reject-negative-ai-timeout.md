---
"posthog-go": patch
---

`Config.Validate` rejects a negative `CaptureAIBatchUploadTimeout` or `CaptureAIMaxQueueSize`. Before, a negative timeout meant AI uploads had no timeout, and a negative queue size quietly became the default. Only zero selects the default.
