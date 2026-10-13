---
"posthog-go": major
---

Make New return (Client, error), like NewWithConfig. Both constructors return a nil client and an error only for invalid configuration. An empty or personal (phx_) project API key gives a disabled client and a nil error, and logs an error. Add Config.Disabled to build a disabled client for tests and local development.
