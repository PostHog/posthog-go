---
"posthog-go": minor
---

posthogmcp: `ReadSessionToken`, `TransportSessionID` and `ResolveClient` read PostHog session tokens and a request's own client identity. Both MCP adapters now give a replayed PostHog session token's session unchanged, identify the client from the request's `_meta` before the session token and `initialize`, and start a new one-client session when a call arrives 30 minutes or more after the last one ended.
