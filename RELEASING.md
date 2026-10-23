# Releasing

This repository uses [Changesets](https://github.com/changesets/changesets) for version management and an automated GitHub Actions workflow for releases.

## How to Release

### 1. Add a Changeset

When making a change that should be released, add a changeset:

```bash
pnpm changeset
```

This prompts you to select the version bump (`patch`, `minor`, or `major`) and write a short release summary. Commit the generated file in `.changeset/` with your pull request.

The repository ships three Go modules that share one release version:

| Go module | Tag |
|---|---|
| `github.com/posthog/posthog-go/v2` | `vX.Y.Z` |
| `github.com/posthog/posthog-go/otel/v2` | `otel/vX.Y.Z` |
| `github.com/posthog/posthog-go/posthogmcpsdk/v2` | `posthogmcpsdk/vX.Y.Z` |

`otel` and `posthogmcpsdk` import the core SDK, so they move in lockstep with `posthog-go`, major version included. Go encodes the major in the import path, so each module's `/vN` suffix must match the core major: a v2+ tag on a `go.mod` without that suffix publishes a version Go refuses to resolve. See [`posthogmcpsdk/README.md`](posthogmcpsdk/README.md) for how CI checks a nested module against the core release it requires.

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

During release preparation, the workflow updates `otel/go.mod` and `posthogmcpsdk/go.mod` to require the new core version. This is a minimum requirement: consumers can still select a newer core version. These dependency updates are included in the automated version-bump commit.

## Troubleshooting

### No changesets found

If the release workflow reports that no changesets were found, make sure your PR includes at least one releasable `.changeset/*.md` file.
