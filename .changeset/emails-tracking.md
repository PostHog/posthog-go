---
"posthog-go": minor
---

A `CodeEmail` now takes `Tracking`, which says what PostHog tracks for the email: `posthog.EmailTrackingOn()`, `posthog.EmailTrackingOff()`, or `posthog.EmailTrackingWith(posthog.EmailTrackingOptions{...})` to set `Opens`, `Clicks` and `Engagement` on their own. Without it, PostHog counts opens and captures engagement events, but doesn't rewrite links to count clicks, because a link in an email sent from code can carry a login or password reset token. Turn clicks on only when the email's links are safe to send through a redirect. Click tracking needs open tracking.

`CaptureEngagement` is deprecated. Use `Tracking` with `EmailTrackingOptions{Engagement: ...}` instead. It still works, but PostHog refuses a send that sets both. The SDK now leaves `capture_engagement` out of the request unless you set it, which PostHog reads the same way.

`Tracking` needs PostHog's API support, which is rolling out. Until it reaches your region, a send that sets it fails with an `*EmailError`.
