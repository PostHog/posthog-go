---
"posthog-go": patch
---

Fix local evaluation falling back to the API for `gt`, `gte`, `lt` and `lte` when the operand is stored as a string. The PostHog API stores a numeric comparison operand as a string, and its filters validation requires one, so most flags using these operators hold a string. `interfaceToFloat` had a case for every numeric type and none for `string`, so the operand was not orderable. The error it returned was not an `InconclusiveMatchError`, so the caller did not try other conditions either and fell back to the API for a flag it could have matched locally. A string operand is now parsed with `strconv.ParseFloat`, matching what the Python, Node, Ruby and PHP SDKs already do. A value that is not a finite number, including `"NaN"` and `"Inf"`, stays not orderable.
