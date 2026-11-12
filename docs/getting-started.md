# Getting started

The Go SDK runs on your server, on Go 1.21 or later.

To send your first event, create a client with your project token and host from your [project settings](https://app.posthog.com/project/settings), then call `Enqueue` with a `Capture`.

The client queues events and sends them in batches, so call `Close` before your program exits, or the last events are lost.

Install the module (Go 1.21 or later):

```bash
go get github.com/posthog/posthog-go
```

Create a client with your project API key, send an event, and close the client to flush the queue:

```go
package main

import "github.com/posthog/posthog-go"

func main() {
	client, _ := posthog.NewWithConfig("<ph_project_api_key>", posthog.Config{
		Endpoint: "<ph_client_api_host>",
	})
	defer client.Close() // flushes queued events

	client.Enqueue(posthog.Capture{
		DistinctId: "user_123",
		Event:      "button_clicked",
		Properties: posthog.NewProperties().Set("plan", "pro"),
	})
}
```

## Events without a distinct ID

`EnqueueWithContext` sends a `Capture` or `Exception` that has no distinct ID as a [personless event](https://posthog.com/docs/data/anonymous-vs-identified-events) when a request context is attached. The SDK generates a UUID as the distinct ID and sets the `process_person_profile` option to `false`. `Enqueue` without a request context still returns an error when `DistinctId` is empty.
