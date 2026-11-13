---
"posthog-go": major
---

`RequestContext.Properties` and `RequestContext.Options` now also apply to `Identify`, `Alias` and `GroupIdentify`, but they do not set `process_person_profile` on identify-style events (`$identify`, `$create_alias`, `$merge_dangerously`, `$groupidentify`, `$set` and `$set_once`) whose distinct ID the SDK did not generate. Those default to `true` unless the message sets it. The context session ID fills `Identify` and `Alias` but never `GroupIdentify`. `GroupIdentify.DistinctId` is now sent, falling back to the request context's distinct ID, then `$<type>_<key>`. An empty or whitespace-only distinct ID is treated as missing.
