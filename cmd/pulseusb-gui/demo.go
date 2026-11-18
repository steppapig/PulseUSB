package main

import (
	"errors"
	"os"
	"sync"
	"time"

	"pulseusb/internal/attach"
	"pulseusb/internal/client"
	"pulseusb/internal/discover"
	"pulseusb/internal/gui"
	"pulseusb/internal/proto"
)

// backend returns the real system, or with PULSEUSB_DEMO set a simulated rig
// so the window can be tried without any hardware.
func backend() gui.Backend {
	if os.Getenv("PULSEUSB_DEMO") == "" {
		return system{}
	}
	return &demo{have: map[string]int{}}
}

type demo struct {
	taken bool
	mu    sync.Mutex
	have  map[string]int
	next  int
}

func (d *demo) List(host string) ([]*proto.Device, error) {
	if host != "192.168.1.50:3240" && host != "192.168.1.77:3240" {
		time.Sleep(3 * time.Second) // like a machine that is switched off
		return nil, errors.New("connection timed out")
	}
	if host == "192.168.1.77:3240" { // a second, smaller server
		return []*proto.Device{{BusID: "1-2", VendorID: 0x04a9, ProductID: 0x1909, Interfaces: []proto.Interface{{Class: 7}}}}, nil
	}
	hid := []proto.Interface{{Class: 3}}
	return []*proto.Device{
		{BusID: "1-1.1", VendorID: 0x1234, ProductID: 0x0001, Interfaces: hid},
		{BusID: "1-1.2", VendorID: 0x1234, ProductID: 0x0002, Interfaces: hid},
		{BusID: "1-1.3", VendorID: 0x046d, ProductID: 0xc52b, Interfaces: hid},
		{BusID: "1-1.4", VendorID: 0x0781, ProductID: 0x5581, Interfaces: []proto.Interface{{Class: 8}}},
		{BusID: "1-1.5", VendorID: 0x0d8c, ProductID: 0x0014, Interfaces: []proto.Interface{{Class: 1}, {Class: 3}}},
	}, nil
}

func (d *demo) Attached() (map[string]int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for k, v := range d.have {
		out[k] = v
	}
	return out, nil
}

func (d *demo) Attach(host, busid string, _, _ bool) error {
	if busid == "1-1.3" {
		return errors.New("device is in use by another client")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	d.have[attach.Key(host, busid)] = d.next
	return nil
}

func (d *demo) Detach(port int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range d.have {
		if v == port {
			delete(d.have, k)
		}
	}
	return nil
}

func (d *demo) Kinds(string) map[string]string {
	return map[string]string{"1-1.1": "Game controller", "1-1.2": "Game controller", "1-1.3": "Keyboard and mouse"}
}

func (d *demo) Names(string) map[string]string {
	return map[string]string{"1-1.1": "Example Racing Wheel Base", "1-1.2": "Example Load Cell Pedals"}
}

// In the demo, the pedals are in use on another computer until taken over.
func (d *demo) Holders(host string) (map[string]client.Holder, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.taken || host != "192.168.1.50:3240" {
		return nil, "192.168.1.20", nil
	}
	return map[string]client.Holder{"1-1.2": {Peer: "192.168.1.30"}}, "192.168.1.20", nil
}

func (d *demo) Release(host, busid string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.taken = true
	return nil
}

func (d *demo) Discover() []discover.Server {
	return []discover.Server{{Name: "simrig", Addr: "192.168.1.50:3240"}, {Name: "workshop-pi", Addr: "192.168.1.77:3240"}}
}
