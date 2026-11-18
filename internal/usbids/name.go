package usbids

import "strings"

// Describe gives the best available name for a device: what the device
// calls itself if known, else the database entry, else the vendor alone.
func Describe(reported string, vendor, product uint16) string {
	if reported = strings.TrimSpace(reported); reported != "" {
		return reported
	}
	v, p := Lookup(vendor, product)
	switch {
	case v != "" && p != "":
		return v + " " + p
	case v != "":
		return v + " device"
	}
	return ""
}
