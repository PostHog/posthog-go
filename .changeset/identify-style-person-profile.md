---
"posthog-go": major
---

Identify-style events (`$identify`, `$create_alias`, `$merge_dangerously`, `$groupidentify`, `$set` and `$set_once`, from `Identify`, `Alias`, `GroupIdentify` or `Capture`) now default to the `process_person_profile: true` option unless the call sets the option or the legacy `$process_person_profile` property. The call's value wins over `DefaultEventOptions`, `DefaultEventProperties` and the request context, so a layered `false` no longer gets these events dropped. A legacy property on the call becomes the option before the layers fill. A `Capture` whose distinct ID the SDK generates keeps `process_person_profile: false`. `BeforeSend` sees the value and can change it; a hook that builds or renames an identify-style event must set the option itself. See `docs/migration-v2.md`.
