# winget package

Manifests for publishing PulseUSB in the Windows Package Manager
([microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs)), so it
can be installed with:

```powershell
winget install steppapig.PulseUSB
```

The package depends on `vadimgrn.usbip-win2`, so winget installs the USB
driver first.

## Submitting a version

On Windows, from a checkout of this repository:

```powershell
winget install Microsoft.WingetCreate
wingetcreate submit --token <github-token> winget\manifests\s\steppapig\PulseUSB\1.0.0
```

The token is a GitHub personal access token with the `public_repo` scope.
`wingetcreate` forks winget-pkgs to your account and opens the pull
request; Microsoft's checks then install and scan the package.

## A new release

```powershell
wingetcreate update steppapig.PulseUSB --version X.Y.Z --urls https://github.com/steppapig/PulseUSB/releases/download/vX.Y.Z/PulseUSB-Setup.exe --submit --token <github-token>
```
