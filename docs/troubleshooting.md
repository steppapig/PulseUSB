# Troubleshooting

## The server is not found

- Both computers must be on the same network segment for discovery.
- Port **3240** (TCP and UDP) must be open on the server. The installer
  opens it in firewalld and ufw.
- You can always add a server by address under **Servers**.

## A device connects but does nothing

Some Linux drivers switch a device into another mode before it is shared.
Logitech Unifying receivers are the known case: the server installer offers
the fix, which disables the `hid_logitech_dj` driver on the server.

## A key seems stuck on Windows

If Win, Ctrl or another key behaves as if held, choose **Release stuck
keys** in the tray menu. PulseUSB normally prevents this when keyboards
move between computers.

## Devices are stuck on another computer

Press **Ctrl+Alt+Shift+F12** on a shared keyboard to return all devices to
the server machine. On the server itself, `sudo pulseusb-server release all`
does the same.

## Audio

- **Microphone whine or clicks:** sound devices use the *Bulk data* receive
  mode by default. Try the other mode under **⋯ → Advanced**.
- **Use a wired network** for audio devices; they are the most
  timing-sensitive kind of USB device.
- **A controller with a headset jack becomes the default sound device**
  (for example a DualSense): select your sound devices by name in your
  applications, or disable the controller's audio in Windows' sound
  settings.

## Windows Defender or SmartScreen warns

PulseUSB is not code-signed, so Windows may warn about the installer the
first time: choose **More info → Run anyway**. Releases are built from this
repository by GitHub Actions; to check a download, compare its SHA-256
(`Get-FileHash PulseUSB-Setup.exe`) with `SHA256SUMS.txt` in the release.

## Laptop as a server

The installer can keep a laptop running with the lid closed. If it still
sleeps, check `journalctl -b | grep -i suspend`.

## Logs

- Server: `journalctl -u pulseusb-server -f`
- Detailed USB traffic: add `-v` to the server's service options
  (troubleshooting only).
- App: **Settings** shows recent connection timings.
