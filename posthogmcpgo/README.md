# PostHog MCP analytics for mcp-go

`posthogmcpgo` captures PostHog MCP analytics for servers built with
[`github.com/mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go). Every
`tools/call` to a registered tool becomes a `$mcp_tool_call` event, with the same properties the
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
you already pass to `server.NewMCPServer`. It installs three things:

- A tool filter, which adds the analytics arguments to listed tools.
- A tool handler middleware, which removes those arguments before the handler
  sees the call.
- Call hooks, which capture each call with the response the client receives.

## Option order

Pass `Instrument` after `server.WithHooks`. `Instrument` adds its hooks to the
server's existing `server.Hooks`, and `WithHooks` replaces the whole set. If
`WithHooks` comes later, your hooks still run and calls are still captured,
but by the middleware, and the error handler receives one error saying so.
The middleware sees neither validator: a call rejected by input validation
sends no event, one rejected by output validation is recorded with the
handler's result, and on stdio, in-process, and SSE the protocol version is
missing.

Servers may share one `server.Hooks`. Each instrumented server captures only
its own calls, and a server without `Instrument` sends nothing.

mcp-go runs tool handler middleware in the order its options are given,
outermost first. Middleware given before `Instrument` sees the analytics
arguments. Middleware given after it sees them removed.

## What is captured

- The tool name, duration, and whether the call failed. A failure is a handler
  that returns an error, or any `IsError` result the client receives. That
  includes the results mcp-go sends when `WithInputSchemaValidation` rejects
  the arguments or `WithOutputSchemaValidation` rejects the tool's structured
  content. The failure's text becomes the error message and a `$exception`
  event. A call to an unknown tool sends no event.
- The request as `$mcp_parameters`, in the JSON-RPC shape
  `{"request": {"method": "tools/call", "params": {"name": ..., "arguments": ...}}}`,
  and the result as `$mcp_response`. Credentials and binary content are
  redacted. Turn either off with `WithCaptureParameters(false)` or
  `WithCaptureResponses(false)`. The error text of a failed call is captured
  either way.
- The session as `$session_id`, always set. On stateful streamable HTTP, the
  `Mcp-Session-Id` becomes `ses_` plus a hash every PostHog MCP SDK computes
  the same way, so replicas and SDKs in other languages agree. On stateless
  streamable HTTP, each call gets its own `ses_<UUIDv7>`. On SSE, the
  transport's session id is hashed the same way. On a one-client connection
  (stdio, in-process), the adapter generates `ses_<UUIDv7>` per mcp-go session
  and starts a new one after 30 minutes without a tool call.
- The client name and version from the `initialize` handshake, when the call
  arrives on the session that made it.
- The protocol version: on HTTP from the `MCP-Protocol-Version` header, and on
  other transports the version the session negotiated in `initialize`.
- On HTTP, the `User-Agent` and `X-Anthropic-Client` headers.
- The tool's description and `_meta.category`, read from the registered tool.
- The agent's intent, from the injected `context` argument described below.
- The model that made the call, from the client's request `_meta`
  (`io.modelcontextprotocol/aiInvocation` or `x-codex-turn-metadata`) when it
  names one, else from the `llm_model` argument described below.

Use `WithIdentity` to attach a distinct ID, groups, and person properties,
`WithProperties` to add event properties, and `WithErrorHandler` to see
instrumentation failures. Analytics errors and panics never change the MCP
response.

The identity and properties resolvers run from mcp-go's call hooks, so they
receive the request's context, not the one your tool handler middleware
builds. Put request-scoped values such as the authenticated user on the
context with `server.WithHTTPContextFunc` or `server.WithStdioContextFunc`,
or read them from the request's headers.

A handler that panics without `server.WithRecovery` sends no event, because
mcp-go then reports neither a result nor an error. With `WithRecovery`, the
panic is captured as a failed call.

## The context argument

Intent is what MCP analytics clusters and reports on, so the adapter adds a
required `context` string to every tool's input schema in `tools/list`, asking
the agent to say in a sentence why it is calling the tool. That text is
captured as `$mcp_intent`, with personal data redacted.

The argument is removed before the tool's handler sees the call, from both
`GetArguments` and `BindArguments`. A tool that declares its own `context`
argument keeps it: the adapter does not advertise or remove it, and its value
is the tool's data, captured in `$mcp_parameters` rather than as the intent. A typed
`InputSchema` stays typed and a `RawInputSchema` stays raw, so a tool filter
given after `Instrument` can still read `InputSchema.Properties`.

The adapter changes a schema only when nothing at its root could reject an
extra property. mcp-go's `WithInputSchemaValidation` checks each call against
the registered schema before any middleware runs, so such a schema would
reject the calls an agent builds from the advertised one. The root may hold
`type: "object"`, `properties`, `required`, `additionalProperties: true`, and annotations such as `title`, `description`, `$schema`,
`$id`, `$comment`, `$defs`, `definitions`, `examples`, and `default`. Any other
root keyword leaves the schema unchanged, so calls to that tool carry no
intent. Examples are `additionalProperties: false` (which
`WithStrictInputSchemaDefault` sets on every tool), `unevaluatedProperties`,
`minProperties` (removing the added arguments would leave fewer properties
than it requires), `maxProperties`, `propertyNames`, `patternProperties`,
`not`, `if`, `$ref`, and `allOf`. A schema that refers back to its root with
`"$ref": "#"` anywhere is also left unchanged, because a required argument
added at the root would apply to every nested object that refers to it.

Task tools, registered with `AddTaskTool`, are listed unchanged and their
calls are not captured, because mcp-go runs no tool handler middleware for
them. A regular tool that supports tasks gets its analytics arguments removed
when called as a task, but the call sends no event: mcp-go answers it with the
task, and the adapter does not observe the task's final status.

## The llm_model argument

MCP has no standard way for a client to say which model is calling, so the
adapter also adds a required `llm_model` string, asking the agent for its
exact model identifier or `"unknown"`. It is handled like `context`: removed
before the handler and left off `$mcp_parameters`, while a tool that declares
its own keeps it as the tool's data. A model named in the client's `_meta` wins over it. Turn
both sources off with `WithCaptureModel(false)`.

## Not yet supported

`posthogmcpsdk` also sends `$mcp_unknown_tool`, `$mcp_input_required`, and
`$mcp_missing_capability` events, anchors stateless conversations with a
`conversation_id` argument, and accepts an intent fallback. This adapter does
not do these yet.

## Compatibility and development

The adapter is tested against mcp-go v0.58.0 and requires Go 1.25.5, mcp-go's
minimum. It is a separate Go module so that the core `posthog-go` module does
not depend on mcp-go. Its `go.mod` replaces `posthog-go` and `posthogmcpsdk`
with their directories in this repository; `posthogmcpsdk` is a test-only
dependency, used to check that both adapters send the same properties.
