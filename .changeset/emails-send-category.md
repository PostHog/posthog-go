---
"posthog-go": minor
---

`CodeEmail` takes a `Category`, the key of one of the project's message categories, such as `"newsletter"`, so code can send marketing email: PostHog skips recipients who opted out, fills `{{ unsubscribe_url }}`, and adds List-Unsubscribe headers.
