// pulseusb-client attaches USB devices exported by pulseusb-server (or any
// USB/IP server) so they appear as locally plugged in.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"pulseusb/internal/attach"
	"pulseusb/internal/client"
	"pulseusb/internal/discover"
	"pulseusb/internal/proto"
	"pulseusb/internal/usbids"
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage:
  pulseusb-client discover                        find servers on this network
  pulseusb-client list   <host[:port]>            show devices a server exports
  pulseusb-client auto   <host[:port]>            attach everything the server
                                                exports and keep it attached
  pulseusb-client attach [-watch] <host[:port]> <busid>
                                                plug in one remote device
  pulseusb-client ports                           show attached devices
  pulseusb-client detach <port>                   unplug an attached device

auto and attach -watch keep running and re-attach after a lost connection.
%s
`, attach.Note)
	os.Exit(2)
}

func orUnknown(name string) string {
	if name == "" {
		return "unknown device"
	}
	return name
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pulseusb-client:", err)
	os.Exit(1)
}

func speedName(s uint32) string {
	switch s {
	case proto.SpeedLow:
		return "low (1.5 Mbit/s)"
	case proto.SpeedFull:
		return "full (12 Mbit/s)"
	case proto.SpeedHigh:
		return "high (480 Mbit/s)"
	case proto.SpeedSuper:
		return "super (5 Gbit/s)"
	case proto.SpeedSuperPlus:
		return "super+ (10 Gbit/s)"
	}
	return "unknown"
}

// keepAttached attaches the wanted devices and re-attaches any that drop.
// With busid empty it follows whatever the server currently exports.
func keepAttached(host, busid string) {
	failed := map[string]string{}
	for ; ; time.Sleep(2 * time.Second) {
		devs, err := client.List(host)
		if err != nil {
			report(failed, host, err)
			continue
		}
		delete(failed, host)
		want := map[string]bool{} // bus id -> streaming device
		for _, d := range devs {
			if busid == "" || d.BusID == busid {
				want[d.BusID] = attach.Streaming(d)
			}
		}
		have, err := attach.Attached()
		if err != nil {
			report(failed, "local", err)
			continue
		}
		delete(failed, "local")
		for b, streaming := range want {
			if _, ok := have[attach.Key(host, b)]; ok {
				continue
			}
			if err := attach.Attach(host, b, true, streaming); err != nil {
				report(failed, b, err)
				continue
			}
			delete(failed, b)
			fmt.Printf("attached %s from %s\n", b, host)
		}
	}
}

// report prints an error once instead of every two seconds.
func report(seen map[string]string, what string, err error) {
	if seen[what] != err.Error() {
		seen[what] = err.Error()
		fmt.Fprintf(os.Stderr, "pulseusb-client: %s: %v\n", what, err)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "discover":
		found, err := discover.Query(time.Second)
		if err != nil {
			fatal(err)
		}
		if len(found) == 0 {
			fmt.Println("no servers found")
		}
		for _, f := range found {
			fmt.Printf("%-20s %s\n", f.Name, f.Addr)
		}
	case "list":
		if len(args) != 1 {
			usage()
		}
		devs, err := client.List(args[0])
		if err != nil {
			fatal(err)
		}
		if len(devs) == 0 {
			fmt.Println("no devices exported")
		}
		names, _ := client.Names(args[0])
		for _, d := range devs {
			fmt.Printf("%-12s %04x:%04x  %s (%s speed)\n", d.BusID, d.VendorID, d.ProductID,
				orUnknown(usbids.Describe(names[d.BusID], d.VendorID, d.ProductID)), speedName(d.Speed))
		}
	case "auto":
		if len(args) != 1 {
			usage()
		}
		keepAttached(args[0], "")
	case "attach":
		fs := flag.NewFlagSet("attach", flag.ExitOnError)
		watch := fs.Bool("watch", false, "stay running and re-attach when the connection drops")
		fs.Parse(args)
		if fs.NArg() != 2 {
			usage()
		}
		if *watch {
			keepAttached(fs.Arg(0), fs.Arg(1))
		}
		streaming := false
		if devs, err := client.List(fs.Arg(0)); err == nil {
			for _, d := range devs {
				streaming = streaming || (d.BusID == fs.Arg(1) && attach.Streaming(d))
			}
		}
		if err := attach.Attach(fs.Arg(0), fs.Arg(1), false, streaming); err != nil {
			fatal(err)
		}
		fmt.Printf("attached %s from %s\n", fs.Arg(1), fs.Arg(0))
	case "ports":
		if err := attach.PrintPorts(); err != nil {
			fatal(err)
		}
	case "detach":
		if len(args) != 1 {
			usage()
		}
		port, err := strconv.Atoi(args[0])
		if err != nil {
			usage()
		}
		if err := attach.Detach(port); err != nil {
			fatal(err)
		}
	default:
		usage()
	}
}
