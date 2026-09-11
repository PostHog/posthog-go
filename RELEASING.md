# Releasing

This repository uses [Changesets](https://github.com/changesets/changesets) for version management and an automated GitHub Actions workflow for releases.

## How to Release

### 1. Add a Changeset

When making a change that should be released, add a changeset:

```bash
pnpm changeset
```

This prompts you to select which package changed, the version bump (`patch`, `minor`, or `major`), and a short release summary. Commit the generated file in `.changeset/` with your pull request.

There are two packages, because the repository ships two independently versioned Go modules:

| Package | Go module | Tags |
|---|---|---|
| `posthog-go` | `github.com/posthog/posthog-go/v2` | `vX.Y.Z` |
| `posthog-go-otel` | `github.com/posthog/posthog-go/otel` | `otel/vX.Y.Z` |

Select only the package your change affects. Each module is tagged only when its own version moves, and their majors are independent — the otel bridge does not import the core SDK, so a core major does not drag otel along with it. Go encodes the major in the import path, so a module at v2+ must have a matching `/vN` suffix in its `go.mod`; tagging `otel/v2.0.0` while `otel/go.mod` says `.../otel` would publish a version Go refuses to resolve.

### 2. Merge the Pull Request

After review, merge the PR to `main`. No GitHub release label is required.

A push to `main` that includes `.changeset/*.md` changes automatically starts the release workflow. The workflow then:

1. Checks for pending changesets
2. Notifies the client libraries team in Slack for approval
3. Waits for approval from a maintainer via the GitHub `Release` environment
4. The workflow applies Changesets, syncs `version.go` when the core version moved, and tags each module whose version changed (`vX.Y.Z` for the core plus a GitHub Release, `otel/vX.Y.Z` for the bridge).
5. Notifies Slack when the release completes or fails

### Manual Trigger

You can also manually trigger the release workflow from the Actions tab with `workflow_dispatch`. Manual runs still require pending changesets.

## Version Bumping

Changesets determines the next version from the committed changeset files:

- **patch**: bug fixes, documentation updates, and internal changes
- **minor**: backwards-compatible features
- **major**: breaking changes

## Troubleshooting

### No changesets found

If the release workflow reports that no changesets were found, make sure your PR includes at least one releasable `.changeset/*.md` file.
