---
"posthog-go": patch
---

Fix `ReloadFeatureFlags` panicking after `Close`. Shutdown closed the poller's reload channel, so a later call (or one overlapping `Close`) sent on a closed channel. A closed client now returns `ErrClosed`, and the poller ignores a reload once it is shutting down.
