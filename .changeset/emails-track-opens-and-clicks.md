---
"posthog-go": minor
---

A `CodeEmail` now takes `TrackOpensAndClicks`. Set it to `posthog.Ptr(false)` to send the email without PostHog's open pixel or click tracking, so PostHog can't count its opens and clicks. It defaults to true, and the SDK leaves the field out of the request unless you set it. It is independent of `CaptureEngagement`, which decides whether engagement events are captured on the recipient's person.

The field needs PostHog's API support, which is rolling out. Until it reaches your region, a send that sets it fails with an `*EmailError`.
