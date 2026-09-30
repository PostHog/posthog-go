# PostHog MCP analytics for the official Go MCP SDK

`posthogmcpsdk` captures PostHog MCP analytics for servers built with
[`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).
Every `tools/call` becomes a `$mcp_tool_call` event, the same event the Python
and TypeScript SDKs send.

```go
client := posthog.New("phc_project_api_key")
defer client.Close()

server := mcp.NewServer(&mcp.Implementation{Name: "weather-server", Version: "1.0.0"}, nil)
posthogmcpsdk.Instrument(server, posthogmcp.New(client), posthogmcpsdk.WithServerInfo("weather-server", "1.0.0"))
```

## What is captured

- The tool name, duration, and whether the call failed. A tool that returns an
  error, or a result with `IsError` set, is a failure, and its text becomes the
  error message and a `$exception` event.
- The request as `$mcp_parameters`, in the JSON-RPC shape
  `{"request": {"method": "tools/call", "params": {"name": ..., "arguments": ...}}}`,
  and the result as `$mcp_response`. Credentials and binary content are
  redacted. Turn either off with `WithCaptureParameters(false)` or
  `WithCaptureResponses(false)`. The error text of a failed call is captured
  either way.
- The MCP session ID, client name and version, and protocol version.
- The tool's description and `_meta.category`, read from `tools/list`.
- The agent's intent, from the `context` argument described below.

Use `WithIdentity` to attach a distinct ID, groups, and person properties, and
`WithProperties` to add event properties.

## The context argument

Intent is what MCP analytics clusters and reports on, so the middleware adds a
required `context` string to every tool's input schema in `tools/list`, asking
the agent to say in a sentence why it is calling the tool. That text is
captured as `$mcp_intent`, with personal data redacted.

The argument is removed before the tool's handler and input validation see
the call, so tools registered with `mcp.AddTool`, whose inferred schemas
reject unknown properties, keep working. Tools that declare their own
`context` argument keep it, and its value is still captured as the intent.
Schemas built from `$ref`, `allOf`, `anyOf`, or `oneOf` are not changed.

Turn it off with `WithContextParameter(false)`.

The middleware learns each tool's schema from `tools/list` and forgets it
when the server sends `notifications/tools/list_changed`, so a tool registered
again with a different schema is handled by the new one. go-sdk sends that
notification about 10ms after the change, so calls in that window still use
the old schema. It notifies only connected sessions, so a tool replaced while
no session is connected keeps its old schema until a `tools/list` result
includes it. A tool added while no session is connected, as on a stateless
server, is recognized within ten seconds, when the middleware lists tools
again.

## Middleware order

`Instrument` adds receiving middleware, and sending middleware that watches for
`notifications/tools/list_changed`. Receiving middleware added before it runs
inside the measured duration; middleware added after it runs outside. To place
the middleware yourself, install both halves that `NewMiddleware` returns:

```go
receiving, sending := posthogmcpsdk.NewMiddleware(analytics)
server.AddReceivingMiddleware(receiving)
server.AddSendingMiddleware(sending)
```

## Compatibility and development

The adapter requires go-sdk v1.6.1 or later and Go 1.25, the SDK's minimum. It
is a separate Go module so that the core `posthog-go` module, which supports Go
1.21, does not depend on the MCP SDK. Its `go.mod` replaces `posthog-go` with
the repository root, so it builds against the local core without a workspace.

Consumers ignore that `replace`, so they build against the `posthog-go`
version this module's `go.mod` requires, and that version must contain
`posthogmcp`. Release in this order:

1. Release the `posthog-go` version that contains the `posthogmcp` changes
   this module needs.
2. Bump this module's `require github.com/posthog/posthog-go` to that version
   and run `go mod tidy`.
3. Tag this module as `posthogmcpsdk/vX.Y.Z`. Go resolves a nested module's
   versions from tags prefixed with its directory.
