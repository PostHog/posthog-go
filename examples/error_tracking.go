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
	client, _ := posthog.NewWithConfig(projectAPIKey, posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
		Endpoint:  endpoint,
	})
	defer client.Close()

	done := time.After(3 * time.Second)
	tick := time.Tick(50 * time.Millisecond)

	for {
		select {
		case <-done:
			fmt.Println("exiting")
			return

		case <-tick:
			exception := posthog.NewDefaultException(
				time.Now(),
				"distinct-id",
				"Enqueued error",
				"Error Description",
			)
			if err := client.Enqueue(exception); err != nil {
				fmt.Println("error:", err)
				return
			}

			exceptionWithProps := posthog.Exception{
				DistinctId: "distinct-id",
				Timestamp:  time.Now(),
				Properties: posthog.Properties{
					"environment": "production",
					"retry_count": 3,
				},
				ExceptionList: []posthog.ExceptionItem{
					{Type: "Enqueued error with custom props", Value: "Error Description"},
				},
			}
			if err := client.Enqueue(exceptionWithProps); err != nil {
				fmt.Println("error:", err)
				return
			}
		}
	}
}

func TestErrorTrackingThroughLogHandler(projectAPIKey, endpoint string) {
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
		posthog.WithPropertiesFn(posthog.SlogAttrsAsProperties),
	))

	done := time.After(3 * time.Second)
	tick := time.Tick(50 * time.Millisecond)

	for {
		select {
		case <-done:
			fmt.Println("exiting")
			return

		case <-tick:
			log.Warn("Log that something broke",
				"error", fmt.Errorf("this is a dummy scenario"),
				"retry_count", 3,
				"endpoint", "/api/v1/users",
			)
		}
	}
}
