---
"posthog-go": major
---

Make New return (Client, error) and propagate initialization errors. Both constructors return a nil client and an error when initialization fails. Remove the implicit no-op client fallback. Handle constructor errors before using or closing the client.
