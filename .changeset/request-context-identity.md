---
"posthog-go": major
---

`RequestContext.Properties` and `RequestContext.Options` now also apply to `Identify`, `Alias` and `GroupIdentify`; the context session ID fills `Identify` and `Alias` but never `GroupIdentify`. `GroupIdentify.DistinctId` is now sent, falling back to the request context's distinct ID, then `$<type>_<key>`. An empty or whitespace-only distinct ID is treated as missing.
