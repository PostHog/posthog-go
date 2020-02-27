package main

import (
	"fmt"

	"github.com/posthog/posthog-go"
)
import "time"

func main() {
	client, _ := posthog.NewWithConfig("Csyjlnlun3OzyNJAafdlv", posthog.Config{
		Interval:  30 * time.Second,
		BatchSize: 100,
		Verbose:   true,
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
			if err := client.Enqueue(posthog.Capture{
				Event:  "Download",
				DistinctId: "123456",
				Properties: map[string]interface{}{
					"application": "PostHog Go",
					"version":     "1.0.0",
					"platform":    "macos", // :)
				},
			}); err != nil {
				fmt.Println("error:", err)
				return
			}
		}
	}
}
