---
"posthog-go": patch
---

Send transactional emails, attachment uploads and email status reads through `/api/messaging/` instead of `/api/projects/@current/messaging/`. PostHog takes the project from the project secret key, and the `@current` routes are going away. A PostHog instance without the `/api/messaging/` routes answers these requests with a 404.
