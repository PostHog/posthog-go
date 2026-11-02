---
"posthog-go": patch
---

The error logged when a `BeforeSend` hook panics now names the event and the panic's type. It no longer includes the panic value, which can carry the event data the hook was removing.
