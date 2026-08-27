---
"posthog-go": minor
---

Report a `$release_id` on `$exception` events when `POSTHOG_RELEASE_ID` is set in the environment. This is the native, deploy-time counterpart to injecting `$release_id` into a web bundle: a build tool creates the release with `posthog-cli release resolve`, launches the app with the printed id in `POSTHOG_RELEASE_ID`, and the SDK stamps it on each exception so the server resolves that exception's release by a direct id lookup — no release name or version has to match anything the app reports. Only exception events carry it (that is where a release is resolved), the variable is read once, and an unset or blank value changes nothing.
