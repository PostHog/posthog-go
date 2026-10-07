---
"posthog-go": major
---

Add `Config.DefaultEventOptions` and `RequestContext.Options`, so capture options can be set for every event and for a request. Request-context and event values now override `Config.DefaultEventProperties` and `Config.DefaultEventOptions`; in 1.x a default property overwrote the event's value. The SDK's personless default and `posthogmcp`'s person opt-out are now sent as the `process_person_profile` option. A default, request-context or event option can override the personless default; a legacy `$process_person_profile` property cannot. See `docs/migration-v2.md`.
