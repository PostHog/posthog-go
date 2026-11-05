---
"posthog-go": patch
---

Aligned `posthogmcpsdk` with the sdk-specs MCP spec. The `get_more_tools` virtual tool now advertises `readOnlyHint`, `idempotentHint`, `destructiveHint` and `openWorldHint` as `false`, since it records data, and no longer sets a title. The injected `context` argument has a shorter description that asks for a few words on why the tool is called, described by role. The injected `llm_model` argument is no longer listed as required. This changes your published tool definitions; if your server is listed in the ChatGPT or Claude directories, those platforms may re-review the updated tools.
