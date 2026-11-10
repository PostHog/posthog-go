---
"posthog-go": patch
---

Raise `github.com/klauspost/compress` to v1.20.2, `github.com/andybalholm/brotli` to v1.2.7, `golang.org/x/text` to v0.41.0, and `golang.org/x/sys` to v0.44.0. The compress and brotli releases fix panics and memory use on crafted input. The root module now requires Go 1.25, which those modules need.
