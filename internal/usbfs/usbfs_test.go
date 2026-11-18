//go:build linux

package usbfs

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"pulseusb/internal/desc"
	"pulseusb/internal/proto"
)

// A full-speed composite device: HID keyboard interface + audio streaming
// interface with two alternate settings.
var blob = []byte{
	18, 1, 0x10, 0x01, 0, 0, 0, 8, 0x6d, 0x04, 0x2b, 0xc5, 0x11, 0x12, 1, 2, 0, 1,
	9, 2, 59, 0, 2, 1, 0, 0xa0, 50,
	9, 4, 0, 0, 1, 3, 1, 1, 0,
	9, 0x21, 0x11, 1, 0, 1, 0x22, 59, 0,
	7, 5, 0x81, 3, 8, 0, 10,
	9, 4, 1, 0, 0, 1, 2, 0, 0,
	9, 4, 1, 1, 1, 1, 2, 0, 0,
	7, 5, 0x02, 1, 64, 0, 1,
}

func TestParseDescriptors(t *testing.T) {
	dev, cfg, err := desc.Parse(blob, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dev.VendorID != 0x046d || dev.ProductID != 0xc52b || dev.BcdDevice != 0x1211 || dev.NumConfigs != 1 || dev.ConfigValue != 1 {
		t.Fatalf("device: %+v", dev)
	}
	if len(dev.Interfaces) != 2 || dev.Interfaces[0] != (proto.Interface{Class: 3, SubClass: 1, Protocol: 1}) || dev.Interfaces[1].Class != 1 {
		t.Fatalf("interfaces: %+v", dev.Interfaces)
	}
	if len(cfg.Ifaces) != 2 || cfg.Endpoints[0x81] != desc.Interrupt || cfg.Endpoints[0x02] != desc.Iso || len(cfg.Endpoints) != 2 || cfg.EndpointIface[0x81] != 0 || cfg.EndpointIface[0x02] != 1 {
		t.Fatalf("config: %+v", cfg)
	}
	// Unconfigured device: still listed, nothing to claim.
	dev, cfg, _ = desc.Parse(blob, 0)
	if len(dev.Interfaces) != 2 || len(cfg.Ifaces) != 0 || dev.ConfigValue != 0 {
		t.Fatalf("unconfigured: %+v %+v", dev, cfg)
	}
}

func TestListFromSysfs(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, b []byte, speed string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		for f, v := range map[string]string{"bConfigurationValue": "1\n", "busnum": "1\n", "devnum": "5\n", "speed": speed + "\n"} {
			os.WriteFile(filepath.Join(dir, f), []byte(v), 0o644)
		}
		os.WriteFile(filepath.Join(dir, "descriptors"), b, 0o644)
	}
	hub := append([]byte{}, blob...)
	hub[4] = desc.ClassHub
	mk("1-1.2", blob, "12")
	mk("1-1", hub, "480")
	mk("usb1", hub, "480")
	os.MkdirAll(filepath.Join(root, "1-1.2:1.0"), 0o755)

	b := &Backend{SysRoot: root}
	devs, err := b.List()
	if err != nil || len(devs) != 1 {
		t.Fatalf("list: %v %+v", err, devs)
	}
	if d := devs[0]; d.BusID != "1-1.2" || d.Speed != proto.SpeedFull || d.DevID() != 1<<16|5 {
		t.Fatalf("device: %+v", d)
	}
	// A device in use is described from memory: its files are not read
	// again (they are removed here to prove it), so polling clients cannot
	// disturb it.
	os.WriteFile(filepath.Join(root, "1-1.2", "product"), []byte("Test Card\n"), 0o644)
	b.busy = map[string]*inUse{"1-1.2": {info: devs[0], name: "Acme Test Card"}}
	os.Remove(filepath.Join(root, "1-1.2", "descriptors"))
	os.Remove(filepath.Join(root, "1-1.2", "product"))
	if again, err := b.List(); err != nil || len(again) != 1 || again[0] != devs[0] {
		t.Fatalf("in-use device not served from memory: %v %+v", err, again)
	}
	if n := b.Names(); n["1-1.2"] != "Acme Test Card" {
		t.Fatalf("in-use name: %v", n)
	}
	b.busy = nil
	if gone, _ := b.List(); len(gone) != 0 {
		t.Fatal("test setup: device should be unreadable once not in use")
	}
	mk("1-1.2", blob, "12")

	// What a device is comes from the HID descriptions of its interfaces,
	// and is remembered for while the device is in use.
	hid := filepath.Join(root, "1-1.2:1.0", "0003:046D:C52B.0001")
	os.MkdirAll(hid, 0o755)
	os.WriteFile(filepath.Join(hid, "report_descriptor"), []byte{0x05, 0x01, 0x09, 0x05, 0xa1, 0x01, 0xc0}, 0o644)
	if k := b.Kinds(); k["1-1.2"] != "Game controller" {
		t.Fatalf("kinds: %v", k)
	}
	os.RemoveAll(hid)
	b.busy = map[string]*inUse{"1-1.2": {info: devs[0]}}
	if k := b.Kinds(); k["1-1.2"] != "Game controller" {
		t.Fatalf("kind forgotten while in use: %v", k)
	}
	b.busy = nil

	b.Match = func(d *proto.Device) bool { return d.BusID == "9-9" }
	if devs, _ := b.List(); len(devs) != 0 {
		t.Fatal("filter ignored")
	}
	if _, _, err := b.Open("1-1.2"); err == nil {
		t.Fatal("opened a device that is not exported")
	}
}

// The structures must match the kernel's layout exactly.
func TestLayout(t *testing.T) {
	want := uintptr(56)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 44
	}
	if got := unsafe.Sizeof(urbHdr{}); got != want {
		t.Fatalf("usbdevfs_urb is %d bytes, want %d", got, want)
	}
	if unsafe.Sizeof(isoDesc{}) != 12 || unsafe.Sizeof(disconnectClaim{}) != 264 {
		t.Fatal("bad iso/disconnect_claim size")
	}
	if unsafe.Sizeof(uintptr(0)) == 8 && iocDirShift == 30 {
		// Values from <linux/usbdevice_fs.h> on x86-64 / arm64.
		for name, c := range map[string][2]uintptr{
			"SUBMITURB":        {ioSubmitURB, 0x8038550a},
			"DISCARDURB":       {ioDiscardURB, 0x0000550b},
			"REAPURBNDELAY":    {ioReapURBNDelay, 0x4008550d},
			"CLAIMINTERFACE":   {ioClaimInterface, 0x8004550f},
			"RELEASEINTERFACE": {ioReleaseIface, 0x80045510},
			"SETINTERFACE":     {ioSetInterface, 0x80085504},
			"SETCONFIGURATION": {ioSetConfig, 0x80045505},
			"IOCTL":            {ioIoctl, 0xc0105512},
			"RESET":            {ioReset, 0x00005514},
			"CLEAR_HALT":       {ioClearHalt, 0x80045515},
			"DISCONNECT_CLAIM": {ioDisconnectClaim, 0x8108551b},
		} {
			if c[0] != c[1] {
				t.Errorf("USBDEVFS_%s = %#x, want %#x", name, c[0], c[1])
			}
		}
	}
}
