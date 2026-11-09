---
"posthog-go": minor
---

`CodeEmail` takes a `Category`, so code can send marketing email in one of the project's message categories. Set it to the category's key, such as `"product-updates"`. Leaving it out, or setting `posthog.EmailCategoryTransactional`, sends transactional email as before. A marketing send honors the recipient's opt-outs, and its body needs the `{{ unsubscribe_url }}` placeholder. PostHog refuses such a send with the error code `unknown_category`, `unsubscribe_link_required` or `recipient_opted_out`.
