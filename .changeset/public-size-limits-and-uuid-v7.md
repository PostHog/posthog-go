---
"posthog-go": major
---

Capture size limits are now configurable, and generated UUIDs are version 7.

- `Config.MaxEventBytes` and `Config.MaxBatchBytes` replace the private 500KB constants, defaulting to `DefaultMaxEventBytes` / `DefaultMaxBatchBytes` (both 500000, unchanged). `MaxBatchBytes` bounds request size; `BatchSize` still bounds event count.
- Event UUIDs and `PostHog-Request-Id` are now UUIDv7, which is time-ordered and matches what capture generates server-side. A caller-supplied `Uuid` is untouched.
- Fixed the SDK reporting version `1.0.0` from any Go test binary, including an application's own. It probed `flag.Lookup("test.v")`, so events captured during a customer's tests were recorded against the wrong `$lib_version`.
