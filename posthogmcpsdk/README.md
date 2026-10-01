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
- The session as `$session_id`, always set. A conversation handle, described
  below, comes first. Next, a transport session ID (streamable HTTP) becomes
  `ses_` plus a hash every PostHog MCP SDK computes the same way, so one
  session served from several languages groups together. On a one-client
  connection (stdio, in-memory), the adapter generates `ses_<UUIDv7>` per
  go-sdk session and starts a new one after 30 minutes without a tool call
  in that session. A stateless HTTP request with neither gets its own.
- The client name and version, and the protocol version. On HTTP, also the
  `User-Agent` and `X-Anthropic-Client` headers, which tell apart the
  products of one vendor that share a client name.
- The tool's description and `_meta.category`, read from `tools/list`.
- The agent's intent, from the `context` argument described below.
- The model that made the call, from the client's request `_meta`
  (`io.modelcontextprotocol/aiInvocation` or `x-codex-turn-metadata`) when it
  names one, else from the `llm_model` argument described below.

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

## The llm_model argument

MCP has no standard way for a client to say which model is calling, so the
middleware also adds a required `llm_model` string, asking the agent for its
exact model identifier or `"unknown"`. It is handled like `context`: removed
before the handler and validation, kept by tools that declare their own, and
left off `$mcp_parameters`. A model named in the client's `_meta` wins over it.
Turn both sources off with `WithCaptureModel(false)`.

The middleware learns each tool's schema from `tools/list` and forgets it
when the server sends `notifications/tools/list_changed`, so a tool registered
again with a different schema is handled by the new one. go-sdk sends that
notification about 10ms after the change, so calls in that window still use
the old schema. It notifies only connected sessions, so a tool added or
replaced while no session is connected, as on a stateless server, is
recognized within ten seconds, when the middleware lists tools again.

## Conversation anchoring

A stateless HTTP request carries no session, so without help every call of
one agent conversation lands in its own `$session_id`. The middleware adds an
optional `conversation_id` string to every tool's input schema. When a call
arrives with neither a transport session nor a valid handle, it appends a new
UUIDv7 handle to the result as a final text block,
`{"conversation_id":"<handle>"}`, and the agent passes it back on later calls.
Tools whose output schema is a plain object also get an optional
`_mcp_instructions` property, and every result of theirs carries the handle
there.

A handle the agent sends is used only if it is a UUIDv7. It becomes
`$mcp_conversation_id`, on the `$exception` of a failed call too, and
`$session_id` is derived from it the way every PostHog MCP SDK derives it, so
replicas and servers in other languages agree. A handle wins over a transport
session. A new handle the result cannot carry, as on a protocol error, is not
recorded.

Like `context`, the argument is removed before the handler and validation
and left off `$mcp_parameters`, tools that declare their own `conversation_id`
keep it, and schemas built from `$ref`, `allOf`, `anyOf`, or `oneOf` are not
changed. Turn it off with `WithConversationID(false)`.

## Middleware order

`Instrument` adds receiving middleware, and sending middleware that watches for
`notifications/tools/list_changed`. Receiving middleware added before it runs
inside the measured duration; middleware added after it runs outside. To place
the middleware yourself, install both halves of what `NewMiddleware` returns:

```go
middleware := posthogmcpsdk.NewMiddleware(analytics)
server.AddReceivingMiddleware(middleware.Receiving)
server.AddSendingMiddleware(middleware.Sending)
```

## Compatibility and development

The adapter requires go-sdk v1.6.1 or later and Go 1.25, the SDK's minimum. It
is a separate Go module so that the core `posthog-go` module, which supports Go
1.21, does not depend on the MCP SDK. Its `go.mod` replaces `posthog-go` with
the repository root, so it builds against the local core without a workspace.

Consumers ignore that `replace`, so they build against the `posthog-go`
version this module's `go.mod` requires, and that version must contain
`posthogmcp`. CI also builds and vets the module with the `replace` dropped, so
an adapter change that needs unreleased core API fails there. Release in this
order:

1. Release the `posthog-go` version that contains the `posthogmcp` changes
   this module needs.
2. Bump this module's `require github.com/posthog/posthog-go` to that version
   and run `go mod tidy`.
3. The release workflow tags this module as `posthogmcpsdk/vX.Y.Z` at the
   same version as `posthog-go`, alongside `otel/vX.Y.Z`. Go resolves a nested
   module's versions from tags prefixed with its directory.
