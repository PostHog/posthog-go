---
"posthog-go": minor
---

A `CodeEmail` takes `Cc`, `Bcc` and `Attachments`. `Cc` and `Bcc` are lists of bare addresses, at most 10 each. An `EmailAttachment` is a `Filename` and its `Content` bytes, with an optional `ContentType` and `ContentID` (to show the file inline as `cid:<ContentID>`); the client sends the bytes Base64 encoded. PostHog takes at most 10 attachments and 512 KB in total, and refuses file types that mailbox providers block. An attachment without a `Filename` or `Content` fails with the code `invalid_attachment` before any request, and the error never holds the file's content. Empty lists are left out of the request. A PostHog version that doesn't take one of these fields yet refuses the send with an error that names the field, instead of sending without it.
