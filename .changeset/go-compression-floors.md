---
"posthog-go": minor
---

Require Go 1.25. `github.com/klauspost/compress` v1.20.2 needs that version, and that release plus `github.com/andybalholm/brotli` v1.2.7 fix panics and memory use on crafted input. `golang.org/x/text` moves to v0.41.0 and `golang.org/x/sys` to v0.44.0.
