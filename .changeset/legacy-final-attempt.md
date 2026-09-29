---
"posthog-go": patch
---

Fix the legacy batch sender waiting out a retry delay after the final failed attempt. The failure callback and `Close` now return once that attempt fails, matching the capture v1 sender.
