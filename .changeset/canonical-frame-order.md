---
"posthog-go": minor
---

Error tracking stack frames are now sent in the canonical cross-SDK wire order: the entry point is first and the crash/capture site is last. Previously frames were sent innermost-first. Coordinated rollout: the ingestion pipeline gates on `$lib_version`, so this ships as a minor release.
