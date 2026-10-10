---
"posthog-go": major
---

Capture v1 requires `$session_id` and `$window_id` to be strings. The SDK sends a value whose JSON form is a string, such as a `string` (including `""`), a named string type or a `uuid.UUID`. It drops any other value and logs a warning that names the key and the value's type, never the value. A `nil` value drops without a warning. In 1.x any value was sent as a property. See `docs/migration-v2.md`.
