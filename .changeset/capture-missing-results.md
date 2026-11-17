---
"posthog-go": patch
---

A 2xx capture response whose `results` is missing, `null` or not an object now fails the whole batch: it is not resent, and each event reaches `Callback.Failure` with a `*CaptureRequestError`, as an unparseable body already did. Before, those events were treated as delivered and no callback fired. `{"results": {}}` is still a valid response.
