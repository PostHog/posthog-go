---
"posthog-go": minor
---

Error tracking stack traces now support server-side symbolication. When the running executable's identity can be determined (GNU build id on Linux, LC_UUID on macOS), the default extractor emits frames with raw instruction addresses and client-expanded inline groups, and exceptions carry a `$debug_images` property, so PostHog can re-symbolicate against debug symbols uploaded with `posthog-cli symbol-sets upload` — including exact inline expansion and source context. Without uploaded symbols the runtime-resolved frames are kept as-is, and when the executable can't be identified the frames keep their previous plain shape.
