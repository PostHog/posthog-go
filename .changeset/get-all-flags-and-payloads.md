---
"posthog-go": minor
---

Adds `Client.GetAllFlagsAndPayloads`, the bulk form of `GetFeatureFlagResult`: one evaluation returns every flag's value and the serialized payload of the value it resolved to, with no per-flag payload lookup. Payloads come from local definitions when the flag was evaluated locally and from the same `/flags` response otherwise; a flag the API could not evaluate contributes neither a value nor a payload.
