# Getting started

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
