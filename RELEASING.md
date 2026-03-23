# Releasing

Releases are semi-automated via GitHub Actions. When a PR with the `release` and a version bump label is merged to `master`, the release workflow is triggered.

You'll need an approval from a PostHog engineer. If you're an employee, you can see the request in the [#approvals-client-libraries](https://app.slack.com/client/TSS5W8YQZ/C0A3UEVDDNF) channel.

## Release Process

1. **Create your PR** with the changes you want to release
2. **Add the `release` label** to the PR
3. **Add a version bump label** that should be either `bump-patch`, `bump-minor` or `bump-major`
4. **Merge the PR** to `master`

Once merged, the following happens automatically:

1. A Slack notification is sent to the client libraries channel requesting approval
2. A maintainer approves the release in the GitHub `Release` environment
3. The version is bumped in `version.go` based on the version label (`patch`, `minor`, or `major`, extracted from the label)
4. The `CHANGELOG.md` is updated with a link to the full changelog
5. Changes are committed and pushed to `master`
6. A git tag is created (e.g., `v1.8.0`)
7. A GitHub release is created with the changelog content
8. Slack is notified of the successful release

Releases are installed directly from GitHub.
