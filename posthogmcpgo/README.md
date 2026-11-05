# PostHog MCP analytics for mcp-go

`posthogmcpgo` captures PostHog MCP analytics for servers built with
[`github.com/mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go). Every
`tools/call` that reaches a registered tool's handler becomes a
`$mcp_tool_call` event, with the same properties the
[`posthogmcpsdk`](../posthogmcpsdk) adapter and the Python and TypeScript SDKs
send.

```go
client := posthog.New("phc_project_api_key")
defer client.Close()

s := server.NewMCPServer("weather-server", "1.0.0",
	server.WithToolCapabilities(true),
	posthogmcpgo.Instrument(posthogmcp.New(client), posthogmcpgo.WithServerInfo("weather-server", "1.0.0")),
)
```

`Instrument` returns a `server.ServerOption`, so it goes in the option list
you already pass to `server.NewMCPServer`. It installs a tool filter, which
adds the analytics arguments to listed tools, and a tool handler middleware,
which captures each call.

## What is captured

- The tool name, duration, and whether the call failed. A handler that returns
  an error, or a result with `IsError` set, is a failure, and its text becomes
  the error message and a `$exception` event.
- The request as `$mcp_parameters`, in the JSON-RPC shape
  `{"request": {"method": "tools/call", "params": {"name": ..., "arguments": ...}}}`,
  and the result as `$mcp_response`. Credentials and binary content are
  redacted. Turn either off with `WithCaptureParameters(false)` or
  `WithCaptureResponses(false)`. The error text of a failed call is captured
  either way.
- The session as `$session_id`, always set. On stateful streamable HTTP, the
  `Mcp-Session-Id` becomes `ses_` plus a hash every PostHog MCP SDK computes
  the same way, so replicas and SDKs in other languages agree. On stateless
  streamable HTTP, each call gets its own `ses_<UUIDv7>`. On a one-client
  connection (stdio, in-process, SSE), the adapter generates `ses_<UUIDv7>`
  per mcp-go session and starts a new one after 30 minutes without a tool
  call.
- The client name and version from the `initialize` handshake, when the call
  arrives on the session that made it. On HTTP, also the protocol version
  from the `MCP-Protocol-Version` header, and the `User-Agent` and
  `X-Anthropic-Client` headers.
- The tool's description and `_meta.category`, read from the registered tool.
- The agent's intent, from the `context` argument described below.
- The model that made the call, from the client's request `_meta`
  (`io.modelcontextprotocol/aiInvocation` or `x-codex-turn-metadata`) when it
  names one, else from the `llm_model` argument described below.

Use `WithIdentity` to attach a distinct ID, groups, and person properties,
`WithProperties` to add event properties, and `WithErrorHandler` to see
instrumentation failures. Analytics errors and panics never change the MCP
response.

## The context argument

Intent is what MCP analytics clusters and reports on, so the adapter adds a
required `context` string to every tool's input schema in `tools/list`, asking
the agent to say in a sentence why it is calling the tool. That text is
captured as `$mcp_intent`, with personal data redacted.

The argument is removed before the tool's handler sees the call, from both
`GetArguments` and `BindArguments`. Tools that declare their own `context`
argument keep it, and its value is still captured as the intent. Schemas
built from `$ref`, `allOf`, `anyOf`, or `oneOf` are not changed.

Schemas that reject unknown properties (`additionalProperties: false`, which
`WithStrictInputSchemaDefault` sets on every tool) are not changed either, so
calls to those tools carry no intent. mcp-go's `WithInputSchemaValidation`
checks each call against the registered schema before any tool handler
middleware runs, so it would reject the added arguments there.

## The llm_model argument

MCP has no standard way for a client to say which model is calling, so the
adapter also adds a required `llm_model` string, asking the agent for its
exact model identifier or `"unknown"`. It is handled like `context`: removed
before the handler, kept by tools that declare their own, and left off
`$mcp_parameters`. A model named in the client's `_meta` wins over it. Turn
both sources off with `WithCaptureModel(false)`.

## Middleware order

mcp-go runs tool handler middleware in the order its options are given,
outermost first. Middleware given before `Instrument` runs outside the
measured duration and sees the analytics arguments; middleware given after
it runs inside, and sees them removed.

## Not yet supported

`posthogmcpsdk` also sends `$mcp_unknown_tool`, `$mcp_input_required`, and
`$mcp_missing_capability` events, anchors stateless conversations with a
`conversation_id` argument, and accepts an intent fallback. This adapter does
not do these yet. Calls that mcp-go rejects before the handler, such as an
unknown tool or failed input validation, send no event.

## Compatibility and development

The adapter is tested against mcp-go v0.58.0 and requires Go 1.25.5, mcp-go's
minimum. It is a separate Go module so that the core `posthog-go` module does
not depend on mcp-go. Its `go.mod` replaces `posthog-go` and `posthogmcpsdk`
with their directories in this repository; `posthogmcpsdk` is a test-only
dependency, used to check that both adapters send the same properties.
