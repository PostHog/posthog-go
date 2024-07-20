# PostHog Go

Please see the main [PostHog docs](https://posthog.com/docs).

Specifically, the [Go integration](https://posthog.com/docs/integrations/go-integration) details.

# Quickstart

Install posthog to your gopath
```bash
$ go get github.com/posthog/posthog-go
```

Go 🦔!
```go
package main

import (
    "os"
    "github.com/posthog/posthog-go"
)

func main() {
    client := posthog.New(os.Getenv("POSTHOG_API_KEY"))
    defer client.Close()

    // Capture an event
    client.Enqueue(posthog.Capture{
      DistinctId: "test-user",
      Event:      "test-snippet",
      Properties: posthog.NewProperties().
        Set("plan", "Enterprise").
        Set("friends", 42),
    })
    
    // Add context for a user
    client.Enqueue(posthog.Identify{
      DistinctId: "user:123",
      Properties: posthog.NewProperties().
        Set("email", "john@doe.com").
        Set("proUser", false),
    })
    
    // Link user contexts
    client.Enqueue(posthog.Alias{
      DistinctId: "user:123",
      Alias: "user:12345",
    })
    
    // Capture a pageview
    client.Enqueue(posthog.Capture{
      DistinctId: "test-user",
      Event:      "$pageview",
      Properties: posthog.NewProperties().
        Set("$current_url", "https://example.com"),
    })
}

```

## Questions?

### [Ask away in the community forum.](https://posthog.com/questions)
