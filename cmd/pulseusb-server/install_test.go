package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

type listBackend struct{ stubBackend }

func (listBackend) List() ([]*proto.Device, error) {
	return []*proto.Device{
		{BusID: "1-3", VendorID: 0x046d, ProductID: 0xc52b},
		{BusID: "1-7", VendorID: 0x0a5c, ProductID: 0x5834},
		{BusID: "1-4", VendorID: 0x0d8c, ProductID: 0x0014},
	}, nil
}

func newTestInstaller(t *testing.T, answers string, active ...string) (*installer, *bytes.Buffer, *[]string) {
	root := t.TempDir()
	out := &bytes.Buffer{}
	var ran []string
	has := map[string]bool{"systemctl": true}
	for _, a := range active {
		has[a] = true
	}
	return &installer{
		root: root, in: bufio.NewReader(strings.NewReader(answers)), out: out, backend: usb.Backend(listBackend{}),
		run: func(name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			ran = append(ran, cmd)
			if cmd == "ufw status" {
				return "Status: active\n", nil
			}
			if name == "firewall-cmd" && !has["firewalld-running"] {
				return "", errors.New("not running")
			}
			return "", nil
		},
		have: func(name string) bool { return has[name] },
	}, out, &ran
}

func TestInstallInteractive(t *testing.T) {
	// Name with a space, devices 1 and 3, yes to lid, yes to Logitech.
	i, out, ran := newTestInstaller(t, "sim rig\n1 3\ny\ny\n", "firewall-cmd", "firewalld-running")
	os.MkdirAll(i.p("/proc/acpi/button/lid/LID0"), 0o755)
	os.MkdirAll(i.p("/sys/module/hid_logitech_dj"), 0o755)
	os.MkdirAll(filepath.Dir(i.p(oldUnit)), 0o755)
	os.WriteFile(i.p(oldUnit), []byte("old"), 0o644)

	if err := i.install(nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	unit, _ := os.ReadFile(i.p(unitPath))
	if !strings.Contains(string(unit), `ExecStart=/usr/local/bin/pulseusb-server -name "sim-rig" -d 046d:c52b -d 0d8c:0014`+"\n") ||
		!strings.Contains(string(unit), "Nice=-10") {
		t.Fatalf("unit:\n%s", unit)
	}
	if st, err := os.Stat(i.p(binPath)); err != nil || st.Mode()&0o111 == 0 || st.Size() < 1000 {
		t.Fatalf("program not installed: %v", err)
	}
	if lid, _ := os.ReadFile(i.p(lidPath)); !strings.Contains(string(lid), "HandleLidSwitch=ignore") {
		t.Fatal("lid setting not written")
	}
	if l, _ := os.ReadFile(i.p(logiPath)); !strings.Contains(string(l), "blacklist hid_logitech_dj") {
		t.Fatal("Logitech setting not written")
	}
	if i.exists(oldUnit) {
		t.Fatal("old service not removed")
	}
	cmds := strings.Join(*ran, "\n")
	for _, want := range []string{"systemctl disable --now usbeth-server", "firewall-cmd --permanent --add-port=3240/tcp",
		"firewall-cmd --permanent --add-port=3240/udp", "systemctl daemon-reload", "systemctl enable --now pulseusb-server"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("did not run %q; ran:\n%s", want, cmds)
		}
	}
	if !strings.Contains(out.String(), "Logitech, Inc. Unifying Receiver") || !strings.Contains(out.String(), "Reboot once") {
		t.Fatalf("output:\n%s", out)
	}

	// Uninstall removes what was installed.
	if err := i.uninstall(); err != nil || i.exists(unitPath) || i.exists(binPath) || i.exists(lidPath) {
		t.Fatalf("uninstall: %v", err)
	}
}

func TestInstallUnattended(t *testing.T) {
	i, out, ran := newTestInstaller(t, "", "ufw")
	if err := i.install([]string{"-yes", "-name", "Rig; rm -rf /", "-all"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	unit, _ := os.ReadFile(i.p(unitPath))
	if !strings.Contains(string(unit), `-name "Rig-rm--rf-" -all`+"\n") {
		t.Fatalf("name not made safe:\n%s", unit)
	}
	if cmds := strings.Join(*ran, "\n"); !strings.Contains(cmds, "ufw allow 3240/tcp") || strings.Contains(cmds, "firewall-cmd --permanent") {
		t.Fatalf("firewall handling:\n%s", cmds)
	}
	if i.exists(lidPath) || i.exists(logiPath) {
		t.Fatal("wrote laptop or Logitech settings on a machine without them")
	}
}
