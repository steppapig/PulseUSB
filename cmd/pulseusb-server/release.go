package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"pulseusb/internal/client"
	"pulseusb/internal/proto"
	"pulseusb/internal/usbids"
)

// releaseCommand handles "release" and "share", run on the server machine:
//
//	pulseusb-server release [device...]   use devices on this machine again
//	pulseusb-server share   [device...]   let other computers have them again
//
// A device is a vendor:product ID (046d:c52b), a bus id (1-4) or "all";
// with none given, every device. A released device is taken from the
// computer using it and stays on this machine until shared again or until
// the server restarts.
func releaseCommand(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	port := fs.String("port", proto.DefaultPort, "port the running server listens on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", *port)
	devs, err := client.List(addr)
	if err != nil {
		return fmt.Errorf("the PulseUSB server is not running on this machine (%v)", err)
	}
	names, _ := client.Names(addr)
	describe := func(d *proto.Device) string {
		n := usbids.Describe(names[d.BusID], d.VendorID, d.ProductID)
		if n == "" {
			n = "unknown device"
		}
		return fmt.Sprintf("%s (%04x:%04x, port %s)", n, d.VendorID, d.ProductID, d.BusID)
	}
	mode := uint32(proto.ReleaseKeep)
	if cmd == "share" {
		mode = proto.ReleaseShare
	}

	specs := fs.Args()
	if len(specs) == 0 || (len(specs) == 1 && strings.EqualFold(specs[0], "all")) {
		if err := client.Release(addr, "", mode); err != nil {
			return err
		}
		if mode == proto.ReleaseKeep {
			fmt.Println("All devices are back on this machine and stay here until: pulseusb-server share")
		} else {
			fmt.Println("All devices can be used by other computers again.")
		}
		return nil
	}
	for _, spec := range specs {
		var hit []*proto.Device
		for _, d := range devs {
			if strings.EqualFold(spec, d.BusID) || strings.EqualFold(spec, fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)) {
				hit = append(hit, d)
			}
		}
		if len(hit) == 0 {
			return fmt.Errorf("no shared device matches %q; see: pulseusb-server -list", spec)
		}
		for _, d := range hit {
			if err := client.Release(addr, d.BusID, mode); err != nil {
				return err
			}
			if mode == proto.ReleaseKeep {
				fmt.Printf("%s is back on this machine; share it again with: pulseusb-server share %s\n", describe(d), spec)
			} else {
				fmt.Printf("%s can be used by other computers again.\n", describe(d))
			}
		}
	}
	return nil
}

func runReleaseCommand(cmd string, args []string) {
	if err := releaseCommand(cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "pulseusb-server:", err)
		os.Exit(1)
	}
}
