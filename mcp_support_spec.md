# MCP analytics support plan

> Status: Plan-bound working specification
> Date: 2026-08-02
> Lifetime: Retain while this implementation and upstreaming plan is active. This
> repository does not otherwise use specifications, and this file is not intended
> to become permanent upstream documentation.

## Source baseline

The behavioral baseline is the custom-dispatcher path in `@posthog/mcp` at
PostHog/posthog-js commit
`57f371e540968afaa8a0fe9aec8a53ef1db6b654` (2026-08-01).

Relevant source files:

- `packages/mcp/src/extensions/posthog-mcp.ts`
- `packages/mcp/src/extensions/posthog-events.ts`
- `packages/mcp/src/extensions/sink.ts`
- `packages/mcp/src/extensions/sanitization.ts`
- `packages/mcp/src/extensions/mcp-payloads.ts`
- `packages/mcp/src/extensions/truncation.ts`
- `packages/mcp/src/extensions/exceptions.ts`
- `packages/mcp/src/extensions/constants.ts`
- `packages/mcp/src/types.ts`

The PostHog MCP event contract is beta. Tests in this repository must pin the
baseline above rather than silently following later JavaScript changes.

Compatibility means preserving the canonical event/property vocabulary and the
supported information mapping. It does not require byte-for-byte reproduction
of JavaScript internals. The explicit Go-specific validation, UTF-8 sizing,
identity precedence, duration, and stack decisions in this specification are
normative and must have their own tests.

The PostHog JavaScript payload sanitization and truncation files state that they
are derived from the AgentCat TypeScript SDK. Go files that adapt substantial
sanitization, recursive truncation, or event-size pruning logic from those files
must carry a short source pointer. The package must include the complete
AgentCat copyright and MIT permission notice in
`mcp/THIRD_PARTY_NOTICES.md`. PostHog event names, property names, public API
design, and idiomatic Go event construction do not receive third-party source
headers merely because they preserve protocol compatibility.

## Objective

Add upstreamable MCP analytics support to `posthog-go` with two independent
layers:

1. A framework-independent package that models and emits PostHog's canonical
   MCP events.
2. An optional adapter for `github.com/modelcontextprotocol/go-sdk/mcp` that
   observes MCP server requests through receiving middleware and translates them
   into the framework-independent model.

The initial end-to-end milestone is one canonical `$mcp_tool_call` event for
each terminal logical `tools/call`, including failures, without changing the MCP
result seen by the caller.

## Architecture

```text
github.com/modelcontextprotocol/go-sdk/mcp
                    |
                    | receiving middleware
                    v
contrib/modelcontextprotocol/go-sdk
                    |
                    | posthogmcp.ToolCall
                    v
github.com/posthog/posthog-go/mcp
                    |
                    | posthog.Capture (+ optional posthog.Exception)
                    v
github.com/posthog/posthog-go
                    |
                    | existing queue, batching, retries, and ingestion
                    v
                 PostHog
```

Dependencies point downward only. The core MCP package must not import an MCP
protocol SDK. A contrib adapter may import the core MCP package and one external
MCP SDK.

## Repository layout

```text
mcp/
  THIRD_PARTY_NOTICES.md
  analytics.go
  constants.go
  event.go
  exception.go
  sanitize.go
  tool_call.go
  truncate.go
  *_test.go
  testdata/

contrib/modelcontextprotocol/go-sdk/
  go.mod
  instrumentation.go
  middleware.go
  options.go
  *_test.go
```

Names may change during implementation, but the package and dependency
boundaries are required.

## Core `mcp` package

### Responsibilities

The core package owns:

- public capture input types;
- canonical MCP event and property constants;
- identity, session, group, and custom-property mapping;
- MCP payload sanitization;
- field and total-event truncation;
- conversion into existing `posthog.Capture` and `posthog.Exception` messages;
- optional exception fan-out; and
- enqueue error reporting.

It does not own:

- MCP request interception;
- MCP server or client lifecycle;
- tool registration or schema mutation;
- MCP session-token minting;
- PostHog HTTP transport, authentication, retries, batching, or shutdown; or
- application identity and privacy policy.

### Proposed API

Use the existing `posthog.EnqueueClient` interface so applications retain
ownership of the PostHog client lifecycle and tests can provide a small fake.

```go
package mcp

type Analytics struct {
    // unexported fields
}

func New(client posthog.EnqueueClient, opts ...Option) *Analytics

func (a *Analytics) CaptureToolCall(call ToolCall) error

func WithExceptionAutocapture(enabled bool) Option
```

Example:

```go
client := posthog.New(projectKey)
analytics := posthogmcp.New(client)

err := analytics.CaptureToolCall(posthogmcp.ToolCall{
    ToolName:   "search_docs",
    DistinctID: "user_123",
    Duration:   42 * time.Millisecond,
    IsError:    false,
})
```

`CaptureToolCall` returns validation and enqueue errors. Applications decide how
to observe those errors. An instrumentation adapter must not return an analytics
error in place of the original MCP result.

The core method intentionally does not accept `context.Context`. MCP identity
and properties must come from `ToolCall`, not from `posthog.RequestContext`.
Messages are queued with `EnqueueClient.Enqueue`; the adapter retains the MCP
request context for its resolver and error-handler callbacks.

### Tool-call input

The initial public input should cover the JavaScript custom-dispatcher model:

```go
type ToolCall struct {
    ToolName        string
    ToolDescription string
    ToolCategory    string

    DistinctID   string
    SessionID    string
    Groups       posthog.Groups
    SetProperties posthog.Properties

    ServerName     string
    ServerVersion  string
    ClientName     string
    ClientVersion  string
    ProtocolVersion string

    Intent       string
    IntentSource IntentSource

    Parameters any
    Response   any

    Duration  time.Duration
    IsError   bool
    Error     error
    ErrorType string

    Properties posthog.Properties
    Timestamp  time.Time
}
```

Field names may be adjusted for Go conventions before the public API is merged.
The supported information and wire mapping must remain equivalent.

### Validation and zero values

An accepted tool call must have a non-blank `ToolName` and a non-negative
`Duration`. Duration zero is valid. `IntentSource`, when set, must be
`context_parameter` or `inferred`. A non-blank intent with no source defaults to
`context_parameter`; a source without a non-blank intent is omitted.

Every accepted call emits `$mcp_duration_ms` and `$mcp_is_error`. Convert the Go
duration to a floating-point millisecond value so sub-millisecond measurements
are not rounded to zero.

When `IsError` is false, ignore `Error` and `ErrorType`. When `IsError` is true:

- use `Error.Error()` as the message when `Error` is non-nil;
- otherwise synthesize `Tool <name> returned an error`; and
- use `ErrorType` when non-blank, otherwise use the stable literal `Error`.

The error type and sanitized, bounded error message are written to the main
tool-call event. This follows the existing `posthog-go` pattern of retaining
useful error details in error-tracking events. Disabling exception fan-out does
not remove them from the main event.

### Main event contract

Every accepted tool call emits a `posthog.Capture` with event name
`$mcp_tool_call`.

Required generated properties:

| Property | Value |
|---|---|
| `$mcp_source` | `posthog_mcp_analytics` |
| `$mcp_resource_name` | tool name |
| `$mcp_tool_name` | tool name |
| `$mcp_duration_ms` | wall-clock milliseconds |
| `$mcp_is_error` | tool failure state |

Optional canonical properties:

- `$session_id`
- `$mcp_tool_description`
- `$mcp_tool_category`
- `$mcp_parameters`
- `$mcp_response`
- `$mcp_error_type`
- `$mcp_error_message`
- `$mcp_intent`
- `$mcp_intent_source`
- `$mcp_server_name`
- `$mcp_server_version`
- `$mcp_client_name`
- `$mcp_client_version`
- `$mcp_protocol_version`
- `$groups`
- `$set`

The event timestamp comes from `ToolCall.Timestamp`, defaulting to the capture
time. Groups must be supplied both through `posthog.Capture.Groups` and the
canonical property-building path where necessary so existing SDK enrichment
does not discard them.

### Identity

Resolve `distinct_id` in this order:

1. Explicit `DistinctID`.
2. `SessionID`.
3. The literal `anonymous`.

When no explicit distinct ID exists, set `$process_person_profile` to `false` so
anonymous MCP sessions do not create person profiles. Apply `SetProperties` as
`$set` only when an explicit identity exists.

The core package must not infer identity from an authorization token, email,
login, header, or framework-specific request. Contrib adapters expose an
application callback for that decision.

### Property precedence

Build generated canonical properties first, then merge caller-supplied
`Properties`. This matches the JavaScript custom-dispatcher behavior, where
custom properties may override generated properties.

Identity-control properties are exceptions to that general rule. After merging
custom properties, re-apply these generated values:

- `$groups` comes only from `ToolCall.Groups` and is supplied through both
  `posthog.Capture.Groups` and the property map;
- `$set` is present only when an explicit `DistinctID` exists and comes only
  from `ToolCall.SetProperties`; and
- `$process_person_profile=false` is forced whenever there is no explicit
  `DistinctID`.

This precedence applies to the messages produced by the core package. Existing
client-level `DefaultEventProperties` and `BeforeSend` behavior still runs after
enqueue and remains the application's final policy layer. Do not change the root
SDK's merge order or add a second public before-send mechanism.

### Sanitization

Sanitize `Parameters`, `Response`, `Intent`, and error messages before enqueue.
The initial port must preserve these JavaScript safety properties:

- redact values below keys matching the sensitive-key pattern;
- redact PostHog API-key patterns in strings;
- redact large base64-like strings;
- replace image and audio content blocks with text markers;
- replace embedded resource blobs with a text marker;
- preserve text and resource-link content after recursive sanitization; and
- return new values rather than mutating caller-owned maps or slices.

Go inputs may be typed structs rather than JavaScript-style objects. Normalize
all caller-owned payloads (`Parameters`, `Response`, `Groups`, `SetProperties`,
and `Properties`) through JSON-compatible representations before recursively
sanitizing. Invalid or non-serializable input, including a panicking custom JSON
marshaler, must produce an error rather than a panic. Error text returned by the
MCP package must not include captured payload values and must be capped at 512
UTF-8 bytes.

For the initial port, use the pinned JavaScript patterns and marker strings. The
large-binary gate is 10,240 UTF-8 bytes in Go. PostHog token redaction matches
`ph[a-z]_` tokens with at least 20 token characters. Sensitive-key matching is
case-insensitive and covers the exact-key set in the pinned
`mcp-payloads.ts`. Keep the image, audio, embedded-resource, unsupported-content,
binary-data, and generic `[redacted]` markers equal to the pinned fixtures.

### Truncation

Match the JavaScript baseline limits:

| Limit | Value |
|---|---:|
| Maximum nesting depth | 10 |
| Maximum collection breadth | 100 |
| Maximum general string length | 32,768 |
| Maximum total event size | 102,400 bytes |
| Maximum intent length | 2,048 |
| Maximum error message length | 2,048 |
| Maximum resource/tool name length | 256 |
| Maximum metadata string length | 256 |
| Maximum stack frames | 50 |

The output must remain valid UTF-8 and JSON. Truncation must prefer retaining
canonical routing, identity, tool name, duration, and failure fields over
parameters and responses.

For Go, string limits are UTF-8 byte limits and include the `...` truncation
marker. Collection breadth includes any truncation marker entry. This is an
intentional Go-safe interpretation of the JavaScript implementation's UTF-16
string operations; parity fixtures must include multibyte Unicode cases.

The 102,400-byte limit applies independently to each core-produced capture or
exception payload immediately before `Enqueue`. Client-added UUID, SDK/system
context, `DefaultEventProperties`, and `BeforeSend` changes are outside this
core guarantee.

Size reduction is deterministic:

1. reduce depth, breadth, and strings within response and parameters;
2. remove response, then parameters, if still necessary;
3. reduce or remove custom properties and `$set`; and
4. return a bounded validation error without enqueueing anything if the
   required routing, identity, group, duration, and failure payload alone cannot
   fit.

Do not rely on iteration order or repeatedly search for the largest string.

### Exception fan-out

When `IsError` is true and exception autocapture is enabled, enqueue a sibling
`posthog.Exception` after the main `$mcp_tool_call` capture.

The default is enabled for JavaScript parity. An option disables it without
removing `$mcp_is_error`, `$mcp_error_type`, or `$mcp_error_message` from the
main event.

Use the existing `posthog.Exception` model and error-tracking serialization. Do
not duplicate the core SDK's stack representation. Sanitize and truncate the
exception message before enqueue. Include bounded MCP context such as tool,
server, client, session, protocol, custom properties, and groups.

Go's standard `error` interface does not carry a portable stack trace. The
initial implementation creates one handled, synthetic `ExceptionItem` with no
fabricated stack. Its type is the resolved MCP error type and its value is the
same sanitized, bounded message used by `$mcp_error_message`. The 50-frame
baseline limit remains reserved for a future explicit stack-bearing error
interface; the first pass does not infer one.

If either enqueue fails, attempt all configured messages and return the joined
errors. Partial enqueue is possible and must be documented.

### SDK identity

The JavaScript package reports `$lib=posthog-node-mcp`. `posthog-go` currently
hardcodes `$lib=posthog-go` in core message serialization.

The initial implementation retains `posthog-go` and identifies MCP events with
`$mcp_source=posthog_mcp_analytics`. A scoped `$lib=posthog-go-mcp` mechanism
would change core serialization and requires explicit maintainer agreement; it
must not block the initial tool-call event.

## `modelcontextprotocol/go-sdk` contrib adapter

### Module isolation

The root `posthog-go` module supports Go 1.21. The current
`github.com/modelcontextprotocol/go-sdk` v1.7.0 module requires Go 1.25.

The contrib adapter must therefore be a nested Go module. It must not add the Go
MCP SDK to the root `go.mod` or raise the root module's minimum Go version.

The nested module creates separate release and dependency-management work. The
core package should be merged and released first; the contrib module can then
require that released root version. During fork development, use a local
workspace or temporary replacement without committing an absolute path.

### Responsibilities

The contrib adapter owns:

- registering receiving middleware on `*mcp.Server`;
- recognizing `tools/call` requests;
- measuring handler duration;
- reading raw request arguments and final tool results;
- classifying protocol and tool-result failures;
- reading session, client, and negotiated-protocol metadata exposed by the Go
  MCP SDK;
- resolving application identity, groups, properties, and tool metadata through
  callbacks; and
- reporting capture failures without changing MCP behavior.

It must not duplicate core event construction, sanitization, truncation, or
PostHog transport behavior.

### Proposed API

Prefer `Instrument` or `AddAnalytics` over `AddTracing`: this package emits
PostHog analytics events, not distributed trace spans.

```go
package posthogmcpsdk

type Recorder interface {
    CaptureToolCall(posthogmcp.ToolCall) error
}

func Instrument(server *mcp.Server, recorder Recorder, opts ...Option) {
    cfg := defaultConfig(recorder)
    for _, opt := range opts {
        opt(cfg)
    }
    server.AddReceivingMiddleware(toolCallMiddleware(cfg))
}
```

Registration occurs after `mcp.NewServer`. It may occur before or after tool
registration because the middleware does not snapshot the tool registry.

### Middleware behavior

For requests other than `tools/call`, call the next handler without additional
work.

For `*mcp.CallToolRequest`:

1. Record the start time.
2. Call the next middleware or handler exactly once.
3. Preserve its result and error unchanged.
4. If the result has `NeedsInput()=true`, return it without capture because the
   logical tool call is not terminal.
5. Build a `posthogmcp.ToolCall` from the request and terminal result.
6. Invoke the configured recorder.
7. Route recorder failures to the configured error handler.
8. Return the original MCP result and error.

A call is a failure when the downstream handler returns an error or the final
`*mcp.CallToolResult` has `IsError=true`. When available, use
`CallToolResult.GetError()` as the underlying error while retaining the public
result for response capture.

The adapter reads:

- tool name and raw arguments from `CallToolRequest.Params`;
- response and `IsError` from `CallToolResult`;
- session ID from `CallToolRequest.Session.ID()`;
- protocol version from `CallToolRequest.ProtocolVersion()`;
- client name/version from `CallToolRequest.ClientInfo()`; and
- authorization token information only through an application-supplied identity
  resolver.

The SDK does not expose a public synchronous lookup for registered tool
descriptions or categories. Use an optional tool-metadata resolver rather than
accessing SDK internals.

Receiving middleware is outside the SDK's built-in multi-round-trip middleware
when registered normally after `mcp.NewServer`. Emit once after the downstream
chain returns for each terminal logical `tools/call`. For older protocols the
SDK may invoke the handler more than once inside its receiving middleware; for
the newer protocol it may return `NeedsInput()` and receive a later retry. Emit
neither internal nor input-required intermediate events.

Do not add cross-request timing state in the initial adapter. Duration covers
the complete internal flow for an older-protocol request handled inside the SDK
middleware. For a newer-protocol input-required flow, it covers only the
terminal retry observed by the adapter.

Do not recover downstream panics in the initial implementation. Analytics must
not change the server's panic semantics. Resolver, recorder, and error-handler
panics are instrumentation failures: recover them after the downstream handler
has returned and preserve the original MCP result. Resolver errors or panics
omit that resolver's contribution, are routed to the error handler where
possible, and do not suppress the base tool-call capture.

### Adapter options

The initial option set should support:

```go
type Identity struct {
    DistinctID    string
    Groups        posthog.Groups
    SetProperties posthog.Properties
}

type ToolMetadata struct {
    Description string
    Category    string
}

type IdentityResolver func(
    context.Context,
    *mcp.CallToolRequest,
) (Identity, error)

type ToolMetadataResolver func(
    context.Context,
    *mcp.CallToolRequest,
) (ToolMetadata, error)

type PropertiesResolver func(
    context.Context,
    *mcp.CallToolRequest,
    *mcp.CallToolResult,
    error,
) (posthog.Properties, error)

type ErrorHandler func(context.Context, error)

WithIdentity(IdentityResolver)
WithToolMetadata(ToolMetadataResolver)
WithCaptureParameters(bool)
WithCaptureResponses(bool)
WithProperties(PropertiesResolver)
WithServerInfo(name, version)
WithErrorHandler(ErrorHandler)
```

Defaults should match the JavaScript package where practical:

- capture parameters: enabled;
- capture responses: enabled;
- session attribution: enabled when the SDK supplies a session ID;
- authenticated user identity: disabled unless a resolver supplies it; and
- capture failures: reported through a no-op-safe error handler, never returned
  as MCP errors.

Exception autocapture is configured on the core `Analytics`, not the adapter.
The default error handler is a no-op so instrumentation remains safe for STDIO
servers. Run resolvers after the downstream handler returns. Invoke them
independently in identity, metadata, then properties order; one resolver failure
must not prevent later resolvers or the base capture. Wrap resolver and recorder
errors with their stage before passing them to `ErrorHandler`.

Applications with stricter privacy requirements can disable parameters,
responses, and core exception fan-out while retaining tool name, duration,
failure state, and the sanitized bounded error type/message on failed calls.

## Testing

### Core package

Use a fake `posthog.EnqueueClient` and inspect the enqueued `posthog.Capture` and
`posthog.Exception` messages. Golden fixtures must assert the serialized
PostHog wire shape rather than only internal Go structs. Normalize volatile UUID,
timestamp, SDK version, Go version, and OS fields before comparing goldens.

Minimum coverage:

- successful minimal tool call;
- required-name, negative-duration, intent-source, and error-state validation;
- complete optional property mapping;
- explicit identity, session fallback, and anonymous fallback;
- person-profile suppression for anonymous calls;
- groups and `$set` behavior;
- custom-property precedence;
- identity-control properties cannot be overridden by custom properties;
- failed call with and without exception fan-out;
- one enqueue failing while the other is still attempted;
- sensitive-key, API-key, binary, and content-block redaction;
- depth, breadth, string, error, and total-size truncation;
- deterministic size-pruning order and irreducible oversize failure;
- valid UTF-8 after truncation;
- caller inputs are not mutated;
- invalid and non-serializable inputs fail without panic;
- multibyte UTF-8 boundaries remain valid and within byte limits; and
- exception items are handled, synthetic, and stackless.

Where possible, port JavaScript test vectors from the pinned source commit and
assert equivalent event names and properties.

### Contrib adapter

Use `modelcontextprotocol/go-sdk` in-memory transports and a fake core recorder
for normal method tests. Use the streamable HTTP transport for session-ID
mapping because the in-memory transport has no session ID. Do not test the
middleware only by invoking its closure directly.

Minimum coverage:

- successful typed tool handler emits once;
- typed handler error converted to `CallToolResult.IsError` emits a failure;
- protocol-level handler error emits a failure and remains unchanged;
- raw parameters and final response are passed when enabled;
- parameter and response capture can be disabled independently;
- session, protocol, and client metadata are mapped;
- identity and metadata resolvers are applied;
- resolver errors do not suppress the base capture or later resolvers;
- unrelated MCP methods do not emit tool calls;
- recorder failure does not alter the MCP response;
- resolver, recorder, and error-handler panics do not alter the MCP response;
- middleware calls the next handler exactly once;
- middleware ordering is documented and verified;
- older-protocol internal multi-round processing emits once; and
- newer-protocol `NeedsInput()` results emit nothing until the terminal retry.

## Downstream Starlogz validation

Starlogz is the first integration target, not part of the upstream packages.

It will:

- retain its existing EventBridge/CloudWatch wide events;
- install the Go SDK contrib middleware for PostHog;
- resolve `distinct_id` from verified `TokenInfo.UserID`;
- disable parameter capture, response capture, and exception autocapture for the
  initial rollout;
- retain the normal sanitized and bounded `$mcp_error_type` and
  `$mcp_error_message` properties on failed main events;
- supply server name, service version, and deployment environment;
- configure the PostHog key and host through optional environment variables;
- treat all capture failures as warnings; and
- validate one `$mcp_tool_call` per invocation in PostHog before expanding the
  event set.

Lambda freeze behavior is a delivery risk for an in-memory background queue.
The first rollout should use a batch size of one and verify the final event in a
low-traffic period. If delivery remains unreliable, propose reusable bounded
flush support separately rather than adding Lambda-specific behavior to MCP
event construction.

## Delivery phases

### Phase 1: Core tool-call events

Work on fork branch `mcp-tool-events`.

Deliver:

- core `mcp` package;
- canonical tool-call capture;
- sanitization and truncation;
- optional exception fan-out;
- public documentation and example;
- parity-oriented unit and golden tests;
- the updated root public-API snapshot and release changeset; and
- targeted AgentCat-derived source pointers and `mcp/THIRD_PARTY_NOTICES.md`.

Candidate upstream commit and PR title:

```text
feat(mcp): add canonical MCP tool-call events
```

### Phase 2: Go MCP SDK adapter

Deliver the nested contrib module after the core API is stable and preferably
released.

The nested module is not covered by root `go test ./...`, `go vet ./...`, race
tests, or the root release tag. Add explicit build, unit, race, and vet CI jobs
using the adapter's minimum Go version. Release it with a subdirectory-prefixed
tag such as `contrib/modelcontextprotocol/go-sdk/v0.1.0`; the root release
workflow does not publish nested modules. During a same-repository development
run, generate an uncommitted `go.work` file rather than committing a filesystem
replacement.

Candidate upstream commit and PR title:

```text
feat(contrib): instrument modelcontextprotocol/go-sdk servers
```

### Phase 3: Starlogz integration

Consume the fork commit, configure Terraform and process lifecycle, deploy to
development, and verify actual PostHog ingestion. Replace the fork dependency
with an upstream release when available.

### Deferred expansion

Consider separate changes for:

- `$mcp_initialize`;
- `$mcp_tools_list`;
- `$mcp_missing_capability`;
- resource and prompt events;
- schema mutation for intent and conversation IDs;
- automatic tool-description/category tracking;
- alternate MCP framework adapters;
- MCP-specific SDK `$lib` identity; and
- reusable flush support for serverless runtimes.

## Acceptance criteria

The initial plan is complete when:

1. The core package produces a `$mcp_tool_call` wire event compatible with the
   pinned JavaScript contract.
2. Safety and size limits are enforced before enqueue.
3. The Go SDK adapter emits exactly once per terminal logical tool call.
4. Analytics failures never replace or modify the MCP result.
5. The root module remains compatible with Go 1.21 and has no MCP SDK
   dependency.
6. Starlogz can consume the fork and observe tool calls in a PostHog development
   project without exporting tool parameters or responses.
7. The core change is focused and documented well enough to submit upstream.
