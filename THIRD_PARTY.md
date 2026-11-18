# Third-party components

PulseUSB builds on the following work.

| Component | Use | Licence |
|---|---|---|
| [usbip-win2](https://github.com/vadimgrn/usbip-win2) | Windows kernel driver that presents remote devices as locally plugged in. Not bundled; installed separately (the app can download its installer). | BSD-2-Clause |
| [usb.ids](http://www.linux-usb.org/usb-ids.html) | USB vendor and product names, embedded as `internal/usbids/usb.ids.gz`. | GPL-2.0-or-later or BSD-3-Clause (used under BSD-3-Clause) |
| [fyne.io/systray](https://github.com/fyne-io/systray) | Tray icon. | Apache-2.0 |
| [go-webview2](https://github.com/jchv/go-webview2) and go-winloader | The app window (embeds Microsoft's WebView2). | MIT |
| [purego](https://github.com/ebitengine/purego) | Loads GTK and WebKitGTK at run time for the Linux window. | Apache-2.0 |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Windows system calls. | BSD-3-Clause |
| [godbus/dbus](https://github.com/godbus/dbus) | Dependency of systray (not used on Windows). | BSD-2-Clause |

The wire protocol is USB/IP, as implemented by the Linux kernel's
`usbip` drivers.
