---
"posthog-go": minor
---

Add the `posthogmcp` package: framework-independent MCP tool-call analytics with payload sanitization,
bounded event sizes, identity mapping, and optional exception fan-out.

An explicit `$process_person_profile: false` on an event now survives `DefaultEventProperties`.
