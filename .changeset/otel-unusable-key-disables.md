---
"posthog-go-otel": minor
---

NewExporter and NewSpanProcessor no longer return an error for an empty or personal (phx_) API key. They log one error and return an exporter or processor that exports nothing, so a missing key never stops the application. An invalid WithHost still returns an error.
