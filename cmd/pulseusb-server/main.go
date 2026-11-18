// pulseusb-server shares USB devices plugged into this machine with USB/IP
// clients on the network.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"pulseusb/internal/discover"
	"pulseusb/internal/licenses"
	"pulseusb/internal/proto"
	"pulseusb/internal/server"
	"pulseusb/internal/usb"
	"pulseusb/internal/usbids"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var devices, allow list
	listen := flag.String("listen", ":"+proto.DefaultPort, "address to listen on")
	all := flag.Bool("all", false, "export every USB device except hubs")
	flag.Var(&devices, "d", "device to export: bus id (1-1.4) or vendor:product (046d:c52b); repeatable")
	flag.Var(&allow, "allow", "only accept clients from this network (CIDR or IP); repeatable")
	name := flag.String("name", "", "name shown to clients that search the network (default: host name)")
	hide := flag.Bool("no-discovery", false, "do not answer clients searching the network for servers")
	escape := flag.String("escape", "Ctrl+Alt+Shift+F12",
		"keys that, pressed on a shared keyboard, give every device back to this machine; pressed here, share them again (\"off\" to disable)")
	verbose := flag.Bool("v", false, "log every USB request and its result (for troubleshooting)")
	lic := flag.Bool("licenses", false, "print the licences of the third-party work in this program and exit")
	show := flag.Bool("list", false, "print the devices that would be exported and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: pulseusb-server [-listen addr] [-allow net]... (-all | -d device...)\n       sudo pulseusb-server install     set up as a service (asks for a name and devices)\n       sudo pulseusb-server uninstall\n       pulseusb-server release [device|all]   use devices on this machine again\n       pulseusb-server share [device|all]     let other computers have them again")
		flag.PrintDefaults()
	}
	// "release" and "share" give devices to this machine and back.
	if len(os.Args) > 1 && (os.Args[1] == "release" || os.Args[1] == "share") {
		runReleaseCommand(os.Args[1], os.Args[2:])
		return
	}
	// "install" and "uninstall" set the server up as a system service.
	if len(os.Args) > 1 && (os.Args[1] == "install" || os.Args[1] == "uninstall") {
		in := realInstaller(newBackend(nil))
		var err error
		if os.Args[1] == "install" {
			err = in.install(os.Args[2:])
		} else {
			err = in.uninstall()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "pulseusb-server:", err)
			os.Exit(1)
		}
		return
	}
	flag.Parse()

	if *lic {
		fmt.Print(licenses.Text)
		return
	}
	if !*all && len(devices) == 0 && !*show {
		flag.Usage()
		os.Exit(2)
	}
	var match func(d *proto.Device) bool
	if !*all && len(devices) > 0 {
		sel := map[string]bool{}
		for _, d := range devices {
			sel[strings.ToLower(d)] = true
		}
		match = func(d *proto.Device) bool {
			return sel[d.BusID] || sel[fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)]
		}
	}
	// Fewer, later garbage-collection cycles: the server's memory use is
	// tiny, and this keeps collector work out of the way of USB traffic.
	debug.SetGCPercent(400)
	backend := lowLatency(newBackend(match), log.Printf)
	if *show {
		devs, err := backend.List()
		if err != nil {
			log.Fatal(err)
		}
		names := map[string]string{}
		if n, ok := backend.(usb.Namer); ok {
			names = n.Names()
		}
		for _, d := range devs {
			fmt.Printf("%-12s %04x:%04x  %s\n", d.BusID, d.VendorID, d.ProductID,
				usbids.Describe(names[d.BusID], d.VendorID, d.ProductID))
		}
		return
	}

	srv := &server.Server{Backend: backend, Logf: log.Printf, Trace: *verbose}
	if !strings.EqualFold(*escape, "off") && *escape != "" {
		c, err := server.ParseCombo(*escape)
		if err != nil {
			log.Fatalf("-escape: %v", err)
		}
		srv.Escape = c
		go srv.WatchLocalKeyboards()
		log.Printf("escape keys: %s", c)
	}
	for _, a := range allow {
		if !strings.Contains(a, "/") {
			if strings.Contains(a, ":") {
				a += "/128"
			} else {
				a += "/" + strconv.Itoa(32)
			}
		}
		_, n, err := net.ParseCIDR(a)
		if err != nil {
			log.Fatalf("-allow: %v", err)
		}
		srv.Allow = append(srv.Allow, n)
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s", l.Addr())
	if !*hide {
		// Discovery answers on the same port number, over UDP.
		tcp := l.Addr().(*net.TCPAddr)
		if *name == "" {
			*name, _ = os.Hostname()
		}
		if pc, err := net.ListenPacket("udp4", net.JoinHostPort("", strconv.Itoa(tcp.Port))); err != nil {
			log.Printf("discovery disabled: %v", err)
		} else {
			go discover.Serve(pc, *name, tcp.Port, srv.Allowed)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("shutting down, returning devices to the host")
		l.Close()
	}()
	srv.Serve(l)
	srv.Shutdown()
}
