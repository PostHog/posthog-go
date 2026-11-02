---
"posthog-go": minor
---

Add `Client.Emails()` to send transactional email and read its status. `Send` takes a `TemplateEmail`, a template made in PostHog sent by its ID with `TemplateData`, or a `CodeEmail`, content your code rendered and named by a `TemplateKey`, with `Subject`, `HTML` and/or `Text`, `From`, `ReplyTo` and `CaptureEngagement`. `Get` reads an email's status by its message ID. Both need `Config.SecretKey`, a project secret API key with the `hog_flow:write` scope, and go to the PostHog app host: `Config.AppHost`, or the app host of a PostHog Cloud `Endpoint`. Every send carries an idempotency key, and network errors, rate limits and an unavailable service are retried with it (`Config.EmailRequestMaxRetries`, default 2). Errors are `*EmailError` values with PostHog's `Code` and a coarse `Name`. `Close` waits for email requests in flight. New `Config` options: `AppHost`, `EmailRequestTimeout`, `EmailRequestMaxRetries` and `MigratedFrom`.

`Client` gains the `Emails` method, so a type that implements `Client` itself needs it too.
