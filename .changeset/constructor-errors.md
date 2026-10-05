---
"posthog-go": major
---

Make New return (Client, error) and propagate initialization errors. Both New and NewWithConfig now return nil and ErrSDKDisabled for a project API key that is empty after trimming whitespace, instead of a no-op client. Handle constructor errors before using or closing the client.
