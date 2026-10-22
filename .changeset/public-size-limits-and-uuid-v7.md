---
"posthog-go": major
---

Capture size limits are now configurable, and generated UUIDs are version 7.

- `Config.MaxBatchBytes` replaces the private 500KB request constant and defaults to `DefaultMaxBatchBytes` (500000, unchanged). `BatchSize` still bounds event count. An event larger than `MaxBatchBytes` is sent alone.

- Analytics events have no per-event size cap by default. In 1.x an event over 500KB was refused with `ErrMessageTooBig`, although PostHog accepts larger events; within PostHog's request limits, PostHog now drops an oversized event on its own and reports it per event. The optional `Config.MaxEventBytes` refuses larger events locally, and `NewWithConfig` then rejects a `MaxBatchBytes` below it.

- Event UUIDs and `PostHog-Request-Id` are now UUIDv7, which is time-ordered and matches what capture generates server-side. A caller-supplied `Uuid` is kept, normalized to the lowercase hyphenated form.

- Fixed the SDK reporting version `1.0.0` from any Go test binary, including an application's own. It probed `flag.Lookup("test.v")`, so events captured during a customer's tests were recorded against the wrong `$lib_version`.
