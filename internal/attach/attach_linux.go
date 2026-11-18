package attach

import (
	"fmt"
	"strings"

	"pulseusb/internal/client"
	"pulseusb/internal/vhci"
)

// Note explains what attaching needs on this platform.
const Note = "Attaching needs root and the vhci-hcd kernel module (modprobe vhci-hcd)."

var controller = &vhci.Controller{}

// Attach plugs a remote device in.
func Attach(host, busid string, once, streaming bool) error {
	_, err := client.Attach(host, busid, controller)
	return err
}

// Attached returns the local port of each remote device currently plugged in.
func Attached() (map[string]int, error) {
	ports, err := controller.Ports()
	if err != nil {
		return nil, err
	}
	have := map[string]int{}
	for _, p := range ports {
		if p.State == vhci.StateFree {
			continue
		}
		if f := strings.Fields(controller.Origin(p.Number)); len(f) == 3 {
			have[f[0]+":"+f[1]+"/"+f[2]] = p.Number
		}
	}
	return have, nil
}

// PrintPorts lists attached devices on standard output.
func PrintPorts() error {
	ports, err := controller.Ports()
	if err != nil {
		return err
	}
	n := 0
	for _, p := range ports {
		if p.State != vhci.StateUsed {
			continue
		}
		n++
		fmt.Printf("port %d: local device %s", p.Number, p.BusID)
		if o := controller.Origin(p.Number); o != "" {
			fmt.Printf(", from %s", o)
		}
		fmt.Println()
	}
	if n == 0 {
		fmt.Println("no devices attached")
	}
	return nil
}

// Detach unplugs the device on a local port.
func Detach(port int) error { return controller.Detach(port) }
