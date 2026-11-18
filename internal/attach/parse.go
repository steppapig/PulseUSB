// Package attach plugs remote USB devices into the local machine, using
// vhci-hcd on Linux and the usbip-win2 driver on Windows.
package attach

import (
	"net"
	"strconv"
	"strings"

	"pulseusb/internal/client"
	"pulseusb/internal/proto"
)

// Key identifies a remote device the same way on every platform.
func Key(host, busid string) string {
	h, p, _ := net.SplitHostPort(client.Addr(host))
	return h + ":" + p + "/" + busid
}

// Streaming reports whether a device moves bulky data (video, storage). On
// Windows such devices are attached in the driver's zero-copy receive mode
// by default and all others, including audio whose data rate is small but
// timing-critical, in its low-latency mode.
func Streaming(d *proto.Device) bool {
	for _, c := range append([]proto.Interface{{Class: d.Class}}, d.Interfaces...) {
		switch c.Class {
		case 0x08, 0x0e: // mass storage, video
			return true
		}
	}
	return false
}

// parsePorts reads "usbip.exe port" output, which has a "Port NN: ..." line
// followed by a "-> usbip://host:port/busid" line per attached device, and
// returns the local port number of each device key.
func parsePorts(out string) map[string]int {
	have := map[string]int{}
	port := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Port "); ok {
			if num, _, ok := strings.Cut(rest, ":"); ok {
				port, _ = strconv.Atoi(strings.TrimSpace(num))
			}
		}
		if _, rest, ok := strings.Cut(line, "usbip://"); ok {
			have[strings.TrimSpace(rest)] = port
		}
	}
	return have
}
