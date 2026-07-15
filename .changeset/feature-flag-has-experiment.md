---
"posthog-go": minor
---

Add a `$feature_flag_has_experiment` boolean property to `$feature_flag_called` events. The value comes from the `has_experiment` field the server reports on flag metadata (`/flags?v=2`) and on local-evaluation flag definitions. The property is only sent when the server explicitly reported `has_experiment` (true or false); it is omitted when unknown (older deployments, v3 responses, or flags missing from the response).
