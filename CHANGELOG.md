# Changelog

## 0.24.2

First public release. Highlights of what PulseUSB does:

- **Server (Linux)**: shares USB devices plugged into it over the network,
  using the standard USB/IP protocol, so every USB request travels
  unchanged. Built-in installer (`pulseusb-server install`) that asks for a
  name and the devices to share, sets up a service, opens the firewall and
  handles laptops with the lid closed. Found automatically by the apps.
- **App (Windows and Linux)**: lists servers and their devices with their
  real names; Connect/Disconnect, auto-connect, your own names, groups that
  connect together, hiding unwanted devices; tray menu and keyboard
  shortcuts (Windows); starts with Windows, minimized, or before sign-in.
- **Several computers**: a device in use on another computer shows as such
  and can be taken over. The escape keys (Ctrl+Alt+Shift+F12) on a shared
  keyboard give everything back to the machine it is plugged into, like a
  KVM switch.
- **Input**: keys are never left "held" when a keyboard moves between
  computers; low-latency handling for wheels, pedals and controllers.
- **Audio**: sound cards and microphones work as if plugged in locally,
  without compression.

Earlier 0.x versions were development builds and were not published.
