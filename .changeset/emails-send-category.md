---
"posthog-go": minor
---

`CodeEmail` takes a `Category`, so code can send marketing email and file email under a message category. Set it to `posthog.MessageCategory{Key: "newsletter", Type: posthog.MessageCategoryMarketing, Name: "Newsletter"}`, which defines the category in code: the first send creates it, and a send with a new `Name` or `Description` updates it. Leave `Category` out for transactional email with no category, as before, or for the category of the template that `TemplateKey` names. `posthog.EmailCategoryTransactional` asks for transactional email with no category, so PostHog refuses it with `category_mismatch` on a template in a category. A marketing send honors the recipient's opt-outs, and its body needs the `{{ unsubscribe_url }}` placeholder. PostHog refuses such a send with the error code `category_mismatch`, `too_many_categories`, `unsubscribe_link_required` or `recipient_opted_out`.
