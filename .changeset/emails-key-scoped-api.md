---
"posthog-go": patch
---

Send transactional emails, attachment uploads and email status reads through `/api/messaging/` instead of `/api/projects/@current/messaging/`. PostHog takes the project from the project secret key, and the `@current` routes are going away. Needs PostHog Cloud, or a self-hosted PostHog from 2026-11-18 or later. Older self-hosted versions answer these calls with a 404.
