//go:build !windows

package main

import "pulseusb/internal/gui"

// On Linux a keyboard that goes away takes its pressed keys with it.
func guardStuckKeys(*gui.Engine) {}
func releaseStuckKeys() []string { return nil }
