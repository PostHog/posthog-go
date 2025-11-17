package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/posthog/posthog-go"
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
			// Capture an error / exception
			exception := posthog.NewDefaultException(
				time.Now(),
				"distinct-id",
				"Error title",
				"Error Description",
			)
			if err := client.Enqueue(exception); err != nil {
				fmt.Println("error:", err)
				return
			}

			// Capture an exception with custom properties
			exceptionWithProps := posthog.NewDefaultException(
				time.Now(),
				"distinct-id",
				"Error title",
				"Error Description",
			).WithProperties(posthog.NewProperties().
				Set("custom_property_a", "custom_value_a").
				Set("custom_property_b", "custom_value_b"),
			)
			if err := client.Enqueue(exceptionWithProps); err != nil {
				fmt.Println("error:", err)
				return
			}

			// Or use the Exception struct directly for full control
			fullControlException := posthog.Exception{
				DistinctId: "distinct-id",
				Properties: posthog.NewProperties().
					Set("custom_property_a", "custom_value_a").
					Set("custom_property_b", "custom_value_b"),
				ExceptionList: []posthog.ExceptionItem{
					{
						Type:  "Error title",
						Value: "Error description",
					},
				},
			}
			if err := client.Enqueue(fullControlException); err != nil {
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
		// Extract custom properties from log attributes
		posthog.WithPropertiesFn(func(ctx context.Context, r slog.Record) posthog.Properties {
			props := posthog.NewProperties()
			r.Attrs(func(a slog.Attr) bool {
				props.Set(a.Key, a.Value.Any())
				return true
			})
			return props
		}),
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
