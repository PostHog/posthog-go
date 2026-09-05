---
"posthog-go": minor
---

Add `Config.FlagDefinitionCacheProvider`, an experimental interface for sharing local evaluation flag definitions between SDK instances through an external cache such as Redis. One instance is elected to poll the PostHog API and publishes the definitions it fetches; the others load them from the cache instead of calling the API. The cached payload carries the flags, group type mapping, cohorts, and the server-controlled `minimal_flag_called_events` gate, so followers emit the same `$feature_flag_called` events as the instance that fetched. Cached definitions that cannot be used are ignored in favour of the definitions already loaded, and closing the client stops the polling loop before releasing the provider, bounded by the deadline passed to `CloseWithContext`.
