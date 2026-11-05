---
"posthog-go": minor
---

The new `Client.Emails()` sends transactional email from your Go code and reads each email's delivery status.

**Breaking for custom clients.** A type that implements `Client` itself now needs an `Emails` method.

```go
client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
if err != nil {
	log.Fatal(err)
}
defer client.Close()

result, err := client.Emails().Send(ctx, posthog.CodeEmail{
	To:          "user@example.com",
	DistinctID:  "user_123",
	TemplateKey: "password-reset",
	Subject:     "Reset your password",
	HTML:        renderedHTML,
})
if err != nil {
	log.Fatal(err)
}

status, err := client.Emails().Get(ctx, result.ID)
fmt.Println(status.Status) // "delivered", "bounced", ...
```

You can also send a template made in PostHog with a `TemplateEmail` and its `TemplateID`.

You need a project secret API key, the kind that starts with `phs_`. Create one under **Project secret API keys** in your project settings. Set its **Transactional email** scope to Write to send emails, or to Read for a key that only reads statuses. Set it as `SecretKey` in `posthog.Config`. Set `AppHost` to your PostHog app URL when you self-host PostHog or send through a reverse proxy. The client also loads feature flags with this key, so give it **Feature flags: Read** too, or it logs an error each time it polls.

PostHog records each email's delivery, opens and clicks on the person with that `DistinctID`. It groups every email with the same `TemplateKey`, so you see their metrics and logs in one place. The SDK retries a request that hits a network error, a rate limit or an outage twice by default. Every send carries an idempotency key, so a retry never sends the email twice. Pass your own `IdempotencyKey`, such as `welcome:user_123`, and your job runner's retries are safe too.

A `CodeEmail` also takes `From`, `ReplyTo` and `CaptureEngagement: posthog.Ptr(false)`. Set `MigratedFrom` in `posthog.Config` to tell PostHog which email provider you moved from. A failed call returns an `*EmailError` with PostHog's `Code` and the HTTP `StatusCode`.

Docs: [Send transactional emails with Go](https://posthog.com/docs/workflows/transactional-emails/go) and the [transactional email overview](https://posthog.com/docs/workflows/transactional-emails).
