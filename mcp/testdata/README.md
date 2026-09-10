# MCP test provenance

The event/property contract and sanitization/truncation cases in this package
are pinned to PostHog/posthog-js commit
`57f371e540968afaa8a0fe9aec8a53ef1db6b654` (2026-08-01), primarily:

- `packages/mcp/src/__tests__/posthog-mcp.test.ts`
- `packages/mcp/src/__tests__/posthog-events.test.ts`
- `packages/mcp/src/__tests__/sanitization.test.ts`
- `packages/mcp/src/__tests__/truncation.test.ts`
- `packages/mcp/src/__tests__/sink.test.ts`

Go-specific validation, UTF-8 byte limits, deterministic size pruning, and
stackless synthetic exceptions intentionally differ from JavaScript behavior as
documented in `mcp_support_spec.md` at the repository root.
