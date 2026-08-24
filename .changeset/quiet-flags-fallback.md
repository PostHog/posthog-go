---
"posthog-go": minor
---

Fall back to `/flags` when a requested flag is missing from loaded local definitions. This changes the earlier behavior where the key was omitted without a request.
