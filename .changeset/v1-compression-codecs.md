---
"posthog-go": minor
---

Add zstd, deflate, and brotli compression for the capture-v1 path, alongside the existing gzip. Select one via `Config.Compression` (`CompressionZstd`, `CompressionDeflate`, `CompressionBrotli`) and the SDK sets the matching `Content-Encoding` header (`zstd`, `deflate`, `br`). These three codecs require `CaptureMode: posthog.CaptureModeAnalyticsV1` — the legacy `POST /batch/` endpoint only understands gzip, so configuring them with `CaptureModeLegacy` returns a validation error. `CompressionGzip` and `CompressionNone` continue to work on both capture modes. All codecs use pure-Go libraries (stdlib `compress/zlib`, `github.com/klauspost/compress/zstd`, `github.com/andybalholm/brotli`), so `CGO_ENABLED=0` builds are unaffected.
