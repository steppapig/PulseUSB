package vhci

import (
	"os"
	"path/filepath"
	"testing"

	"pulseusb/internal/proto"
)

const status = `hub port sta spd dev      sockfd local_busid
hs  0000 006 003 00010007 000003 3-1
hs  0001 004 000 00000000 000000 0-0
ss  0008 004 000 00000000 000000 0-0
`

func TestPortsAndAttach(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644)
	os.WriteFile(filepath.Join(dir, "attach"), nil, 0o644)
	c := &Controller{Dir: dir, StateDir: filepath.Join(dir, "run")}
	ports, err := c.Ports()
	if err != nil || len(ports) != 3 {
		t.Fatalf("%v %+v", err, ports)
	}
	if p := ports[0]; p.Hub != "hs" || p.State != StateUsed || p.DevID != 0x10007 || p.BusID != "3-1" {
		t.Fatalf("%+v", p)
	}
	for speed, want := range map[uint32]string{proto.SpeedHigh: "1 9 65543 3", proto.SpeedSuper: "8 9 65543 5"} {
		if _, err := c.Attach(9, 0x10007, speed); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "attach")); string(got) != want {
			t.Fatalf("attach wrote %q, want %q", got, want)
		}
	}
	c.Record(1, "pi", "3240", "1-1.2")
	if c.Origin(1) != "pi 3240 1-1.2" {
		t.Fatal("record")
	}
	// Old kernels: no hub column.
	if p, ok := parseLine("0002 004 000 00000000 0000000000000000 0-0"); !ok || p.Number != 2 || p.State != StateFree {
		t.Fatalf("old format: %+v", p)
	}
	if _, ok := parseLine("prt sta spd dev socket local_busid"); ok {
		t.Fatal("header parsed as port")
	}
}
