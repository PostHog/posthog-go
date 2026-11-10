package posthog_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/posthog/posthog-go"
)

// A template made transactional in PostHog, sent by its ID. The secret key is
// a project secret API key with the transactional_email:write scope.
func ExampleEmails_template() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{
		Endpoint:  "https://eu.i.posthog.com",
		SecretKey: "phs_...",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	result, err := client.Emails().Send(context.Background(), posthog.TemplateEmail{
		To:             "user@example.com",
		TemplateID:     "0190f6d2-7c1e-7b3a-9f5e-1c2d3e4f5a6b",
		TemplateData:   map[string]interface{}{"name": "Ada"},
		DistinctID:     "user_123",
		IdempotencyKey: "welcome:user_123",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("queued", result.ID)
}

// Content your code rendered, named by a key. Render a React email to HTML in
// your app first, for example with @react-email/render, and pass the HTML.
func ExampleEmails_code() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	renderedHTML := "<p>Reset your password: https://example.com/reset/abc</p>"
	_, err = client.Emails().Send(context.Background(), posthog.CodeEmail{
		To:          "user@example.com",
		TemplateKey: "password-reset",
		Subject:     "Reset your password",
		HTML:        renderedHTML,
		DistinctID:  "user_123",
	})

	var emailErr *posthog.EmailError
	if errors.As(err, &emailErr) && emailErr.Code == "recipient_suppressed" {
		fmt.Println("skipped a suppressed recipient:", emailErr.Fix)
	}
}

// Count clicks on an email whose links carry no login or reset token. Clicks
// are off by default for content sent from code.
func ExampleEmails_tracking() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	_, err = client.Emails().Send(context.Background(), posthog.CodeEmail{
		To:          "user@example.com",
		TemplateKey: "weekly-digest",
		Subject:     "Your weekly digest",
		HTML:        "<p>Read this week's posts: https://example.com/blog</p>",
		Tracking:    posthog.EmailTrackingWith(posthog.EmailTrackingOptions{Clicks: posthog.Ptr(true)}),
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Read what happened to a sent email, for 30 days after the send.
func ExampleEmails_Get() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	status, err := client.Emails().Get(context.Background(), "01a18356-c56c-0001-e494-7666bc915188")
	if err != nil {
		log.Fatal(err)
	}
	if status.Status == "bounced" {
		fmt.Println("bounced:", status.Reason)
	}
}
