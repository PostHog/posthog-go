---
"posthog-go": minor
---

Send the same email to many recipients with `Emails.Batch`, one email each, from a `TemplateEmailBatch` with `TemplateData` per recipient or a `CodeEmailBatch`. The client sends at most 100 recipients and 4 MiB of JSON per request, one request after another, and waits at least 45 seconds for each answer. Each `BatchRecipient` has its own `IdempotencyKey`, given or made by the client, that every retry reuses. `SendBatchResult` holds a `BatchEmailResult` per recipient, in order, with `Status` `"queued"`, `"failed"`, `"unconfirmed"` or `"not_attempted"`; send an unconfirmed email again with the same key within 24 hours, so it goes out once. A request that fails as a whole stops the batch and returns its `*EmailError` with a result that holds every recipient, after reading each email a lost answer left unconfirmed by its key. `batch_unavailable` (409) and a `not_found` batch say to send each email with `Send`.
