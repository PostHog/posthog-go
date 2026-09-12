---
"posthog-go": patch
---

Fix a data race between `Enqueue` and `Close`, and stop losing events silently.

- `Enqueue` racing `Close` was a send on a closed channel — a data race, not merely the panic the SDK recovered from. Any application whose graceful shutdown overlaps an enqueue could trip the race detector in its own test suite. Sends are now ordered against shutdown, and still return `ErrClosed` as before.

- A capture failure that reaches no `Callback` now logs one aggregate line per batch instead of nothing. This covers terminal responses (`400`, `401`, `402`, `413`, `415`) and events the server drops inside a `200`. Previously a client with a bad API key and no `Callback` configured saw complete silence.

  The line carries a count and a cause, never per-event detail: per-event logging scales with event volume rather than request volume, and payloads may carry sensitive content. Registering a `Callback` suppresses it, since the callback already reports every failure.
