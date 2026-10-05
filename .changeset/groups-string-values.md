---
"posthog-go": major
---

`Groups` is now `map[string]string`, and `Groups.Set` takes a string value. Convert a numeric group ID with `strconv.FormatInt`, which keeps its feature flag bucketing. See [the migration guide](docs/migration-v2.md#groups).
