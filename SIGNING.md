# Signing the Windows releases

Releases are signed through [SignPath Foundation](https://signpath.org/)'s
free code signing for open-source projects (see the
[code signing policy](CODE_SIGNING_POLICY.md)). The build workflow signs
automatically for version tags once the repository is configured; until
then, and for all other builds, it skips signing.

## One-time setup (maintainer)

1. **Two-factor authentication** on the GitHub account (required by
   SignPath Foundation for everyone on the project).
2. **Apply** at [signpath.org](https://signpath.org/) for the project
   `steppapig/PulseUSB`, giving the repository URL and the link to
   `CODE_SIGNING_POLICY.md`.
3. **Once accepted**, in SignPath:
   - make sure the project's slug is `PulseUSB` and the release signing
     policy's slug is `release-signing` (or set the variables below to the
     actual slugs);
   - connect the project to GitHub Actions as its trusted build system;
   - set the project's artifact configuration to the contents of
     `.signpath/artifact-configuration.xml`;
   - create an API token for a user that may submit signing requests.
4. **In GitHub**, under the repository's Settings → Secrets and variables →
   Actions:
   - secret `SIGNPATH_API_TOKEN`: the API token;
   - variable `SIGNPATH_ORGANIZATION_ID`: the SignPath organization ID;
   - optional variables `SIGNPATH_PROJECT_SLUG`,
     `SIGNPATH_SIGNING_POLICY_SLUG`, `SIGNPATH_ARTIFACT_CONFIGURATION_SLUG`
     if the defaults above differ.

## Each release

Create the release with a new `v…` tag. The build then submits two signing
requests to SignPath: first `PulseUSB.exe` and `pulseusb-client.exe`, then
`PulseUSB-Setup.exe` (built around the signed app). Approve each in
SignPath; the build waits up to an hour for each approval, then publishes
the signed files with the release.
