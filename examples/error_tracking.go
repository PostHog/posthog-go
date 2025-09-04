package main

import (
	"context"
	"fmt"
	"github.com/posthog/posthog-go"
	"log/slog"
	"os"
	"time"
)

func TestErrorTrackingThroughEnqueueing(projectAPIKey, endpoint string) {
	fmt.Println("📊 Error tracking => Through Enqueueing...")

	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
		Endpoint:  endpoint,
	})
	defer client.Close()

	fmt.Println("→ Sending 'Exception' event...")
	exception := posthog.NewDefaultException(
		time.Now(),
		"distinct-id",
		"Enqueued error",
		"Error Description",
	)
	if err := client.Enqueue(exception); err != nil {
		fmt.Println("❌ Error sending `Exception` event:", err)
		return
	}

	// Give the client time to send events
	time.Sleep(1 * time.Second)
	fmt.Println("✅ Exception sent successfully through 'enqueueing'!")
}

func TestErrorTrackingThroughLogHandler(projectAPIKey, endpoint string) {
	fmt.Println("📊 Error tracking => Through Log Handler...")

	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
		Endpoint:  endpoint,
	})
	defer client.Close()

	baseLogHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(posthog.NewSlogCaptureHandler(baseLogHandler, client,
		posthog.WithDistinctIDFn(func(ctx context.Context, r slog.Record) string {
			// for demo purposes, real applications should likely pull this value from the context.
			return "my-user-id"
		}),
	))

	fmt.Println("→ Sending 'Exception' event...")
	log.Warn("Log that something broke",
		"error", fmt.Errorf("this is a dummy scenario"),
	)

	// Give the client time to send events
	time.Sleep(1 * time.Second)
	fmt.Println("✅ Exception sent successfully through 'log handler'!")
}
