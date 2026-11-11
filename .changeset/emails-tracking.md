---
"posthog-go": minor
---

A `CodeEmail` now takes `Sensitive` and `Tracking`.

`Sensitive` marks an email that carries a login link, a reset token or a code. PostHog keeps a sensitive email's links unchanged, leaves its subject off engagement events and keeps its content out of logs. Without it, a code send takes the setting of the saved template whose key matches its `TemplateKey`, or is sensitive when no template has that key, as before.

`Tracking` says what PostHog tracks for the email: `posthog.EmailTrackingOn()`, `posthog.EmailTrackingOff()`, or `posthog.EmailTrackingWith(posthog.EmailTrackingOptions{...})` to set `Opens`, `Clicks` and `Engagement` on their own. Without it, PostHog counts opens, captures engagement events, and counts clicks only when the email isn't sensitive. Turn clicks on only when the email's links are safe to send through a redirect. Click tracking needs open tracking.

`CaptureEngagement` is deprecated. Use `Tracking` with `EmailTrackingOptions{Engagement: ...}` instead. It still works, but PostHog refuses a send that sets both. The SDK now leaves `capture_engagement` out of the request unless you set it, which PostHog reads the same way.

`Sensitive` and `Tracking` need PostHog's API support, which is rolling out. Until it reaches your region, a send that sets either fails with an `*EmailError`.
