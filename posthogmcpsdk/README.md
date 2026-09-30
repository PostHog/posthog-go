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
  `WithCaptureResponses(false)`.
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

## Middleware order

`Instrument` adds receiving middleware. Middleware added before it runs inside
the measured duration; middleware added after it runs outside.

## Compatibility and development

The adapter requires go-sdk v1.6.1 or later and Go 1.25, the SDK's minimum. It
is a separate Go module so that the core `posthog-go` module, which supports Go
1.21, does not depend on the MCP SDK. Its `go.mod` replaces `posthog-go` with
the repository root, so it builds against the local core without a workspace.
