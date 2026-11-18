//go:build !windows

package main

import "pulseusb/internal/gui"

// Global shortcuts are offered on Windows only: on Linux, desktops (and
// Wayland in particular) do not let programs take keys system-wide.
func runHotkeys(*gui.Engine) {}
