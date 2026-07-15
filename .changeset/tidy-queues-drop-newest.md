---
"posthog-go": minor
---

Drop the newest event instead of blocking when the in-memory queue is full, and make the queue size configurable.

- `Enqueue` now returns `ErrQueueFull` (and invokes `Callback.Failure`) when the queue is full, dropping the newest message rather than blocking the caller until space frees up. This matches posthog-python and posthog-rs. **Backfill/bulk callers**: check the error returned by `Enqueue` for `ErrQueueFull` and throttle or retry (or raise `MaxQueueSize`); otherwise events that overflow the queue are dropped, not delayed.
- Add `Config.MaxQueueSize` (default `DefaultMaxQueueSize` = 10000) to control the in-memory message queue capacity independently of `BatchSize`. It is clamped up to `BatchSize` so the queue always holds at least one full batch. This replaces the previous hardcoded `BatchSize * 10` sizing.
- Change the default `BatchSize` (`DefaultBatchSize`) from 250 to 100, aligning with posthog-python, posthog-node, and posthog-rs. Callers that set `BatchSize` explicitly are unaffected.
