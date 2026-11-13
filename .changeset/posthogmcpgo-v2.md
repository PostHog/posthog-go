---
"posthog-go": minor
---

`posthogmcpgo` moves to `github.com/posthog/posthog-go/posthogmcpgo/v2` with posthog-go 2.0.0, and adds `posthogmcpgo.WithEventOptions`, which works like `posthogmcpsdk.WithEventOptions`. Its events report `$lib` `posthog-go-mcp` with the SDK version, like the other MCP adapters. See `docs/migration-v2.md`.
