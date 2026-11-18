# Reference

## Server options

```bash
pulseusb-server -list                       # show shareable devices
pulseusb-server -all                        # share every device except hubs
pulseusb-server -d 046d:c52b -d 1-1.4       # share devices by vendor:product or port
pulseusb-server -name office-pi             # the name the apps show
pulseusb-server -allow 192.168.1.0/24       # only accept these computers
pulseusb-server -escape Ctrl+Alt+Shift+F12  # the KVM-style keys ("off" disables)
pulseusb-server -no-discovery               # do not answer network searches
pulseusb-server -v                          # log every USB request
pulseusb-server release [device|all]        # use devices on the server machine again
pulseusb-server share [device|all]          # let other computers have them again
```

The service's options are on the `ExecStart` line of
`/etc/systemd/system/pulseusb-server.service`. Run the installer again to
change the setup, or `sudo pulseusb-server uninstall` to remove it.

While a client uses a device, the server keeps the processor out of deep
sleep states so wake-up delays do not reach your inputs.

## Command-line client

`pulseusb-client` does what the app does, for scripts: `discover`, `list`,
`attach`, `auto`, `ports` and `detach`. On Windows it needs the usbip-win2
driver; on Linux, root and the `vhci-hcd` kernel module.

## Settings files

| Component | Location |
|---|---|
| Windows app | `%AppData%\PulseUSB\config.json` |
| Linux app (installed as a service) | `/etc/pulseusb/config.json` |
| Linux app (run directly) | `~/.config/PulseUSB/config.json` |
| Server | none; options are on the service's command line |

## How it works

```
 USB devices ──▶ Linux server ══ USB/IP over TCP ══▶ Windows or Linux computer
                 (usbfs)                              (usbip-win2 / vhci-hcd)
```

The server takes a device from its own Linux drivers through usbfs and
forwards its USB traffic over TCP in the USB/IP format. On Windows, the
[usbip-win2](https://github.com/vadimgrn/usbip-win2) driver presents the
device to the system as if it were plugged in locally; on Linux, the
kernel's `vhci-hcd` driver does.

PulseUSB adds a few protocol extensions (device names and kinds, which
computer uses a device, taking devices over). Other USB/IP software ignores
them, so the server also works with the standard `usbip` tools.

## Building from source

Requires Go 1.23 or later. Everything builds without cgo.

```bash
CGO_ENABLED=0 go build -o bin/ ./cmd/pulseusb-server ./cmd/pulseusb-client
```

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H=windowsgui" -o bin/PulseUSB.exe ./cmd/pulseusb-gui
```

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/ ./cmd/pulseusb-server
```

The Windows installer is built with NSIS from `installer/pulseusb.nsi`; the
GitHub workflow does this and bundles the usbip-win2 installer. Icon and
version details come from `cmd/pulseusb-gui/app.rc`; the logo is drawn by
`tools/logo.py`.

Run the tests with `go test -race ./...`.
