---
"posthog-go": minor
---

Add a `$feature_flag_has_experiment` boolean property to every `$feature_flag_called` event. The value comes from the `has_experiment` field the server reports on flag metadata (`/flags?v=2`) and on local-evaluation flag definitions, and defaults to `false` when the server does not send it.
