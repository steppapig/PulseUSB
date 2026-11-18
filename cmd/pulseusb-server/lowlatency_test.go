package main

import (
	"os"
	"path/filepath"
	"testing"

	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

type stubBackend struct{}
type stubDevice struct{ closed int }

func (stubBackend) List() ([]*proto.Device, error) { return nil, nil }
func (stubBackend) Names() map[string]string       { return map[string]string{"1-1": "x"} }
func (stubBackend) Open(string) (usb.Device, *proto.Device, error) {
	return &stubDevice{}, &proto.Device{}, nil
}
func (d *stubDevice) Submit(*usb.Transfer, func(*usb.Transfer)) error { return nil }
func (d *stubDevice) Cancel(*usb.Transfer)                            {}
func (d *stubDevice) Close() error                                    { d.closed++; return nil }

func TestLowLatencyHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qos")
	os.WriteFile(path, nil, 0o644)
	a := lowLatency(stubBackend{}, t.Logf).(*awake)
	a.path = path
	if n, ok := usb.Backend(a).(usb.Namer); !ok || n.Names()["1-1"] != "x" {
		t.Fatal("names not passed through")
	}
	d1, _, _ := a.Open("1-1")
	d2, _, _ := a.Open("1-2")
	if data, _ := os.ReadFile(path); a.hold == nil || len(data) != 4 || data[0]+data[3] != wakeLatencyUs {
		t.Fatalf("request not made: %v", data)
	}
	d1.Close()
	d1.Close() // closing twice must not release the other device's hold
	if a.hold == nil {
		t.Fatal("released while a device is still attached")
	}
	d2.Close()
	if a.hold != nil || a.active != 0 {
		t.Fatal("not released after the last device")
	}
	// The real kernel interface, where this machine has it.
	real := lowLatency(stubBackend{}, t.Logf).(*awake)
	// Only where this process may make the request (root, usually; not on
	// a build server).
	if f, err := os.OpenFile(qosDevice, os.O_WRONLY, 0); err == nil {
		f.Close()
		d, _, _ := real.Open("1-1")
		if real.hold == nil {
			t.Fatal("kernel refused the low-latency request")
		}
		d.Close()
	}
}
