---
"posthog-go": minor
---

Add `PersonPropertyKeysKnownAbsent` and `GroupPropertyKeysKnownAbsent` to `FeatureFlagPayload`, `FeatureFlagPayloadNoKey`, `EvaluateFlagsPayload` and `SendFeatureFlagsOptions`. They list property keys you know a person or group doesn't have. Local evaluation treats a listed key as absent, even when you also pass a value for it: `is_not_set` and the negated operators (`is_not`, `not_icontains`, `not_starts_with`, `not_ends_with`, `not_regex`) match, and every other operator doesn't, with no `/flags` fallback. Group keys only apply to their own group type. Remote `/flags` requests send the lists as `person_property_keys_known_absent` and `group_property_keys_known_absent`. Each list allows at most 1000 keys, per group type for groups. A longer list returns a `ConfigError` without evaluating or sending the request.
