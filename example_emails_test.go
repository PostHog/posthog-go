package posthog_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/posthog/posthog-go/v2"
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

// A receipt with a copy to finance and the PDF attached. Send uploads the PDF
// to PostHog's file storage unless PostHog already has it, then sends the
// email.
func ExampleEmails_attachments() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	receiptPDF, err := os.ReadFile("receipt.pdf")
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Emails().Send(context.Background(), posthog.CodeEmail{
		To:          "user@example.com",
		TemplateKey: "receipt",
		Subject:     "Your receipt",
		HTML:        "<p>Thanks for your order. Your receipt is attached.</p>",
		Cc:          []string{"finance@example.com"},
		Attachments: []posthog.EmailAttachment{
			{Filename: "receipt.pdf", Content: receiptPDF, ContentType: "application/pdf"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Mark an email that carries no login link, reset token or code as not
// sensitive. PostHog then counts its clicks too, as it does for a template
// send that isn't sensitive.
func ExampleEmails_sensitive() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{SecretKey: "phs_..."})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	_, err = client.Emails().Send(context.Background(), posthog.CodeEmail{
		To:          "user@example.com",
		TemplateKey: "weekly-digest",
		Subject:     "Your weekly digest",
		HTML:        `<p><a href="https://example.com/blog">Read this week's posts</a></p>`,
		Sensitive:   posthog.Ptr(false),
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Choose what PostHog tracks, whatever the email's sensitivity. This digest
// counts clicks but captures no engagement events for the recipient.
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
		HTML:        `<p><a href="https://example.com/blog">Read this week's posts</a></p>`,
		Tracking:    posthog.EmailTrackingWith(posthog.EmailTrackingOptions{Clicks: posthog.Ptr(true), Engagement: posthog.Ptr(false)}),
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

// trialEnds stands in for your database, where the time each user's trial
// ends is stored once, when the trial starts.
var trialEnds = map[string]time.Time{"user_123": time.Now().UTC().Truncate(time.Minute).AddDate(0, 0, 5)}

func storedTrialEnd(userID string) time.Time {
	return trialEnds[userID]
}

// Send a reminder a day before a trial ends. The time comes from a stored
// fact, so a rerun of the job within 24 hours sends the same time with the
// same idempotency key and gets the first send's answer. Only a client on
// PostHog Cloud schedules emails.
func ExampleEmails_scheduled() {
	client, err := posthog.NewWithConfig("phc_...", posthog.Config{
		Endpoint:  "https://eu.i.posthog.com",
		SecretKey: "phs_...",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	trialEndsAt := storedTrialEnd("user_123")
	result, err := client.Emails().Send(context.Background(), posthog.TemplateEmail{
		To:             "user@example.com",
		TemplateID:     "0190f6d2-7c1e-7b3a-9f5e-1c2d3e4f5a6b",
		IdempotencyKey: "trial-ending:user_123",
		ScheduledAt:    trialEndsAt.Add(-24 * time.Hour),
	})
	var emailErr *posthog.EmailError
	if errors.As(err, &emailErr) && emailErr.Code == "schedule_not_confirmed" {
		log.Fatalf("read email %s before sending it again: %s", emailErr.MessageID, emailErr.Fix)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Status, result.ID, result.ScheduledAt)
}
