---
"posthog-go": patch
---

The error logged when an AI event is over the size limit now names the event, as other capture log lines do. It still never includes the event's content.
