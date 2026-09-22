---
"posthog-go": minor
---

Add `Client.Flush` and `Client.FlushWithContext` to send queued events without closing the client. Flush waits for the covered delivery attempts to complete or enter retry backoff, while cancellation stops only the caller's wait. Retryable events remain with the transport for scheduled retries; Flush does not bypass backoff. Delivery failures continue to use the configured retry policy and failure callbacks.

The new methods extend the exported `Client` interface. Custom implementations and mocks of that interface must implement both methods. Existing callers using the SDK constructors can continue using the same client after flushing.
