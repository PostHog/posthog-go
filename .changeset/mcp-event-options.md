---
"posthog-go": minor
---

Add `posthogmcpsdk.WithEventOptions`, which sets capture options, such as `process_person_profile`, on `$mcp_tool_call` and its `$exception` through `posthogmcp.ToolCall.EventOptions`. The options override the request context's. A call without a distinct ID is still sent with `process_person_profile` set to `false`, and `BeforeSend` can still change it. A resolver error or panic goes to the error handler, and the call is captured without these options. See `docs/migration-v2.md`.
