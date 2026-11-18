//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAppInstall(t *testing.T) {
	out := &bytes.Buffer{}
	var ran []string
	libs := false // the window's libraries appear once the package is installed
	i := &appInstaller{
		root: t.TempDir(), out: out,
		run: func(name string, args ...string) (string, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			switch name {
			case "modprobe":
				return "module not found", errors.New("exit 1")
			case "uname":
				return "6.8.0-45-generic\n", nil
			case "apt-get":
				if strings.Contains(strings.Join(args, " "), "webkit") {
					libs = true
				}
			}
			return "", nil
		},
		// A Debian-style system: apt-get, no pacman.
		have:        func(name string) bool { return name == "apt-get" || name == "systemctl" },
		libsMissing: func() bool { return !libs },
	}
	if err := i.install(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	unit, _ := os.ReadFile(i.p(appUnit))
	if !strings.Contains(string(unit), "ExecStart=/usr/local/bin/pulseusb -service -config /etc/pulseusb/config.json\n") {
		t.Fatalf("unit:\n%s", unit)
	}
	if st, err := os.Stat(i.p(appBin)); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("program: %v", err)
	}
	if icon, _ := os.ReadFile(i.p(appIcon)); len(icon) < 1000 || string(icon[1:4]) != "PNG" {
		t.Fatal("icon not installed")
	}
	if d, _ := os.ReadFile(i.p(appDesktop)); !strings.Contains(string(d), "Exec=/usr/local/bin/pulseusb\n") {
		t.Fatal("launcher not installed")
	}
	if m, _ := os.ReadFile(i.p(appModules)); string(m) != "vhci-hcd\n" {
		t.Fatal("kernel module not set to load at boot")
	}
	cmds := strings.Join(ran, "\n")
	for _, want := range []string{"modprobe vhci-hcd", "apt-get install -y linux-modules-extra-6.8.0-45-generic",
		"apt-get install -y libwebkit2gtk-4.1-0", "systemctl enable pulseusb", "systemctl restart pulseusb"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("did not run %q", want)
		}
	}
	if strings.Contains(cmds, "pacman") || strings.Contains(out.String(), "needs GTK 3") {
		t.Fatalf("wrong package manager used, or libraries still reported missing:\n%s\n%s", cmds, out)
	}
	if !strings.Contains(out.String(), "vhci-hcd kernel module could not be loaded") || !strings.Contains(out.String(), "installed and running") {
		t.Fatalf("output:\n%s", out)
	}
	if err := i.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(i.p(appBin)); err == nil {
		t.Fatal("not removed")
	}
}
