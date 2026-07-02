---
"posthog-go": minor
---

Add `NativeStackTraceExtractor` and `NewNativeException` for server-side symbolication. Captured frames carry raw instruction addresses and the executable is reported as a `$debug_images` entry, so PostHog can resolve frames (including exact inline expansion and source context) against debug symbols uploaded with `posthog-cli symbol-sets upload`. Frames keep the runtime-resolved function/file/line as a fallback, and are sent in wire order (outermost first). This is opt-in and requires a PostHog version that understands `platform: "native"` frames; the default extractor is unchanged.
