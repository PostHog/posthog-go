# PostHog adapter for the official Go MCP SDK

This module instruments servers built with
`github.com/modelcontextprotocol/go-sdk/mcp` and records terminal `tools/call`
requests through `github.com/posthog/posthog-go/mcp`.

```go
analytics := posthogmcp.New(posthogClient)

posthogmcpsdk.Instrument(server, analytics,
    posthogmcpsdk.WithServerInfo("weather-server", "1.0.0"),
    posthogmcpsdk.WithIdentity(resolveIdentity),
)
```

Parameter and response capture are enabled by default. Disable either at the
adapter when the application has stricter privacy requirements. Exception
autocapture is configured on the core `posthogmcp.Analytics` recorder.

Receiving middleware wraps in registration order. Install this adapter after
middleware whose duration should be included, and before middleware whose work
should be excluded.

## Compatibility

The adapter currently pins the latest stable official SDK, v1.6.1, and supports
its MCP protocol versions through 2025-11-25. The SDK v1.7 line and the
sessionless 2026-07 protocol are pre-release; support for input-required
multi-round tool calls will be added after that API stabilizes.

This is a nested Go module because the official SDK requires Go 1.25 while the
root PostHog module supports Go 1.21. During same-repository development, create
an uncommitted workspace from the repository root:

```sh
go work init . ./contrib/modelcontextprotocol/go-sdk
```

The adapter has its own tests, CI job, dependency updates, release notes, and
subdirectory-prefixed tags such as
`contrib/modelcontextprotocol/go-sdk/v0.1.0`. Before publishing, its `go.mod`
must require the released root `posthog-go` version that contains the core MCP
package.
