# Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io/),
certificate by [SignPath Foundation](https://signpath.org/).

## What is signed

The Windows programs published on this repository's
[releases page](https://github.com/steppapig/PulseUSB/releases): `PulseUSB.exe`,
`PulseUSB-Setup.exe` and `pulseusb-client.exe`. They are built from the
source in this repository by GitHub Actions (`.github/workflows/build.yml`)
and signed only after a team member has approved the signing request for
that release. Nothing built outside that workflow is signed.

The setup program also carries the installer of the
[usbip-win2](https://github.com/vadimgrn/usbip-win2) driver, unchanged and
signed by that project. PulseUSB's signature does not cover it.

## Team roles

- Committers and reviewers: [steppapig](https://github.com/steppapig)
- Approvers: [steppapig](https://github.com/steppapig)

All team members use multi-factor authentication for GitHub and SignPath.

## Privacy policy

This program will not transfer any information to other networked systems
unless specifically requested by the user or the person installing or
operating it. Specifically:

- The app looks for PulseUSB servers on the local network (a broadcast that
  servers answer) and connects to the servers you choose, to list and use
  their USB devices. That traffic stays between your computers.
- If the usbip-win2 driver is missing and was not bundled with the setup
  program, the app downloads its installer from that project's GitHub
  releases when you install the driver.
- Nothing else: no telemetry, no update checks, no accounts.
