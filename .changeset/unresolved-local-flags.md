---
"posthog-go": minor
---

Report flags that local evaluation could not resolve on `EvaluateFlags` snapshots. `FeatureFlagEvaluations.UnresolvedFlags()` maps each such flag to an `UnresolvedFlagReason` (`experience_continuity`, `unsupported_definition`, `unresolved_dependency` or `missing_context`), and `InconclusiveMatchError` and `RequiresServerEvaluationError` carry the reason in a new `Reason` field. When definitions load, log one warning per flag that local evaluation can never resolve (experience continuity, a static cohort, an unknown operator, a malformed value, or a missing or circular flag dependency), and again only when its definition changes.

Behavior changes:

- A condition with a filter that local evaluation can't check and a filter that does not match now resolves locally as no match. Before, such a flag fell back to a remote request, or stayed unresolved with `OnlyEvaluateLocally`.
- An inactive flag with experience continuity enabled now resolves to `false` locally instead of falling back to a remote request.
- `$feature_flag_called` events from snapshot reads of these flags report `local_evaluation_inconclusive` instead of `flag_missing`.
- The load-time warning is logged with `Logger.Warnf`, so it shows without debug logging.
