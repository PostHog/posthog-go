---
"posthog-go": patch
---

Default capture retry waits now vary randomly by up to 25% either way, so clients that fail together do not all retry at the same moment.
