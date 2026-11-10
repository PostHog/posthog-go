# Releasing

This repository uses [Changesets](https://github.com/changesets/changesets) for version management and an automated GitHub Actions workflow for releases.

## How to Release

### 1. Add a Changeset

When making a change that should be released, add a changeset:

```bash
pnpm changeset
```

This prompts you to select the version bump (`patch`, `minor`, or `major`) and write a short release summary. Commit the generated file in `.changeset/` with your pull request.

### 2. Merge the Pull Request

After review, merge the PR to `main`. No GitHub release label is required.

A push to `main` that includes `.changeset/*.md` changes automatically starts the release workflow. The workflow then:

1. Checks for pending changesets
2. Notifies the client libraries team in Slack for approval
3. Waits for approval from a maintainer via the GitHub `Release` environment
4. The workflow applies Changesets, syncs `version.go` and the nested modules' core requirements, then publishes the core Git tag and GitHub Release before tagging the nested modules at that same release commit.
5. Notifies Slack when the release completes or fails

### Manual Trigger

You can also manually trigger the release workflow from the Actions tab with `workflow_dispatch`. Manual runs still require pending changesets.

## Version Bumping

Changesets determines the next version from the committed changeset files:

- **patch**: bug fixes, documentation updates, and internal changes
- **minor**: backwards-compatible features
- **major**: breaking changes

The core SDK, `otel`, and `posthogmcpsdk` share a release version. During release preparation, the workflow updates `otel/go.mod` and `posthogmcpsdk/go.mod` to require that core version. This is a minimum requirement: consumers can still select a newer core version. These dependency updates are included in the automated version-bump commit.

## Troubleshooting

### No changesets found

If the release workflow reports that no changesets were found, make sure your PR includes at least one releasable `.changeset/*.md` file.

### Failed or cancelled release

The independent [release failure notification workflow](.github/workflows/release-failure-notification.yml) checks every completed main-branch release attempt and alerts if a job failed, timed out, or was cancelled. This includes runner startup failures and cancellations with job records. It also alerts when the run failed but the `Bump version and release` job never appears in the job list, for example when GitHub Actions stops mid-run. Once the GitHub release exists, alerts describe follow-up problems rather than claiming the release failed, and the workflow still alerts if the released Slack notification was skipped. It only acts on runs that a push or a manual dispatch started in this repository.

Cancelled runs with no jobs are ignored, including superseded queued runs and manual cancellations before any job is created. A `Bump version and release` failure before any steps ran is also ignored when the rejection-reply step confirms an explicit rejection in the same attempt and no other job failed.

Job inspection retries transient GitHub API failures up to three times. If inspection still fails, it logs the HTTP status or error type and message, then posts a fallback alert with the overall run status and logs link so the release can be checked manually. Error diagnostics stay in the run log, not in the Slack message.

Alerts reply to the approval thread in the release approvals Slack channel. The release workflow preserves the thread timestamp in a run artifact for 30 days, independently of release approval. Failed-job reruns reuse the latest available thread from the same or an earlier attempt. If the timestamp is missing, invalid, or unavailable, the alert falls back to a new channel message, so it does not depend on the approval notification succeeding. The completion workflow must be present on the default branch to run.
