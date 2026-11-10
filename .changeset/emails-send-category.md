---
"posthog-go": minor
---

`CodeEmail` takes a `Category`, so code can send marketing email and file email under a message category. Set it to `posthog.MessageCategory{Key: "newsletter", Type: posthog.MessageCategoryMarketing, Name: "Newsletter"}`, which defines the category in code: the first send creates it, and a send with a new `Name` or `Description` updates it. Leaving `Category` out, or setting `posthog.EmailCategoryTransactional`, sends transactional email as before. A marketing send honors the recipient's opt-outs, and its body needs the `{{ unsubscribe_url }}` placeholder. PostHog refuses such a send with the error code `category_mismatch`, `too_many_categories`, `unsubscribe_link_required` or `recipient_opted_out`.
