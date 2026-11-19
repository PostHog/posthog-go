---
"posthog-go": patch
---

`posthogmcp` no longer keeps part of an identifier that a long `$mcp_intent` or unknown tool name had cut in half before redaction. That text was cut at twice its limit before the redaction passes ran. A fragment such as `ada.lovelace@exampl` or `phc_Ab3dEf6hIj9` no longer matched its pattern, and once redactions earlier in the text shrank it, the fragment fell inside the kept limit. Long text now follows the sdk-specs bounded scan: it is redacted cut at its limit plus 1,024 bytes and cut at its limit, and only the common prefix is kept. An intent that is base64 data is now judged on its whole length, so it becomes the binary placeholder instead of its first 2,045 characters.
