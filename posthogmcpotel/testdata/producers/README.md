# Producer fixtures

Each directory holds real OTLP trace exports from one producer version, recorded by
`internal/record/record.sh`. Every file is one `ExportTraceServiceRequest` in protojson.
All identities, keys, servers and payloads are synthetic: stub MCP servers, a stub REST API,
and a throwaway JWT signing key generated per run.

Re-record a version with:

```bash
AGENTGATEWAY_BIN=/path/to/agentgateway internal/record/record.sh
```

## agentgateway-1.6.0

One Virtual MCP (`prefixMode: never`) over three targets: `alpha` and `beta` (MCP servers) and
`billing` (an OpenAPI spec). Strict JWT auth. Tracing sets `posthog.distinct_id`,
`posthog.groups.organization` and `posthog.set.email` from `jwt.*`, and
`posthog.mcp.tool_is_error` from `mcp.tool.result.isError`.

`drive.sh` runs one session: `initialize`, `tools/list`, then these `tools/call`s.

| Tool | Outcome at the client | SERVER span |
|---|---|---|
| `search_docs` | result | `posthog.mcp.tool_is_error=false` |
| `lookup_order` | result, `isError: true` | `posthog.mcp.tool_is_error=true` |
| `update_order` | JSON-RPC error -32602, HTTP 200 | `mcp.error.code=-32602`, `mcp.error.message`, status unset |
| `crash_order` | upstream HTTP 502 | `error.type=MCP`, `http.response.status_code=502`, status ERROR |
| `beta_ping` | result from the `beta` target | as `search_docs`, `mcp.target=beta` |
| `get_invoice` (found) | OpenAPI 200 | `posthog.mcp.tool_is_error=false` |
| `get_invoice` (missing) | OpenAPI 404, returned as a successful result | `posthog.mcp.tool_is_error=false` |
| `create_ticket` | OpenAPI 500, JSON-RPC error -32603 | `error.type=MCP`, HTTP 500, status ERROR, no `mcp.error.code` |
| `no_such_tool` | JSON-RPC error -32603, HTTP 500 | no `gen_ai.tool.name`, `error.type=MCP` |
| `search_docs` with a `-00` traceparent | result | dropped unless `parentNotSampled` is set |
| any call without a token | HTTP 401 | `POST /*`, `error.type=JwtAuth`, no `mcp.*` |

`all-signals.otlp.json` uses default sampling and no filter. `filtered.otlp.json` adds
`clientSampling`, `parentNotSampled`, `filter: mcp.tool.target != "beta"`, and an
`mcpAuthorization` rule that denies `lookup_order`.

What the recordings show:

- Every request has one SERVER span. Each `tools/call` also produces CLIENT spans: a
  `tools/list` to every target, then one `tools/call` to the owning target. In
  `all-signals`, 9 `tools/call` SERVER spans come with 35 CLIENT spans that carry
  `mcp.method.name`, 8 of them with `gen_ai.tool.name`.
- `jwt.*` and `mcp.tool.result.isError` evaluate in `tracing.attributes`.
- agentgateway also emits `jwt.sub` on every authenticated SERVER span by default.
- `mcp.session.id` on `initialize` differs from the id every later span in the session carries.
- `filter` on `mcp.tool.target` drops the matching calls. Spans where the expression has no
  `mcp.tool` (initialize, tools/list, unauthenticated) are dropped too, since the filter
  fails closed.
- An `mcpAuthorization` deny is HTTP 400 with `error.type=MCP`, and no `mcp.error.code`.
- The span status message can carry the upstream response body.
- Resource `service.name` is `agentgateway`, not the Virtual MCP's name.
