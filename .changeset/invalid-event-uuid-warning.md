---
"posthog-go": patch
---

An invalid event `Uuid` is still replaced with a generated one, and the SDK now logs one warning when it does. The warning names the rule and not the value, because callers can put their own data in the field. An empty `Uuid` is generated without a warning. The same rule now applies to a `Uuid` set in `BeforeSend`. Before, an invalid or empty `Uuid` from the hook was sent as is, and capture rejected the whole batch.
