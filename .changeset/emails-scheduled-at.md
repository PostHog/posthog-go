---
"posthog-go": minor
---

Schedule a transactional email with `ScheduledAt` on `TemplateEmail` and `CodeEmail`, at most 7 days ahead. The client rounds the time up to the next millisecond and sends the same time on every retry. `SendEmailResult` gains `Status` (`"scheduled"` or `"queued"`) and `ScheduledAt`, and `Emails.Get` reads `"scheduled"` until the email goes out. Only PostHog Cloud schedules emails: on any other app host, such as a self-hosted PostHog or a reverse proxy, `Send` returns `scheduling_requires_cloud` before any request. A scheduled send whose answer doesn't confirm the time, or says the email goes out now while the time is more than a minute after the send's first attempt on the client's clock, also on a same-key replay, returns `schedule_not_confirmed`, with the accepted email's ID in the new `EmailError.MessageID`.
