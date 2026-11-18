package main

import (
	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
	"pulseusb/internal/usbfs"
)

func newBackend(match func(*proto.Device) bool) usb.Backend {
	return &usbfs.Backend{Match: match}
}
