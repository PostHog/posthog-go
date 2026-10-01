---
"posthog-go": minor
---

`posthogmcpsdk` captures the calling model as `$mcp_llm_model`, from the client's request `_meta` or an injected
`llm_model` argument, and the HTTP `User-Agent` and `X-Anthropic-Client` headers as `$mcp_client_user_agent` and
`$mcp_vendor_client`. Turn model capture off with `WithCaptureModel(false)`.
