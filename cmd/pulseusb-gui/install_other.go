//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"pulseusb/internal/gui"
)

// On Linux the app runs as a system service, because plugging a remote
// device in needs root. "install" sets that up; anyone on the machine then
// opens the settings page from the application menu.

const (
	appBin      = "/usr/local/bin/pulseusb"
	appUnit     = "/etc/systemd/system/pulseusb.service"
	appConfig   = "/etc/pulseusb/config.json"
	appModules  = "/etc/modules-load.d/pulseusb.conf"
	appDesktop  = "/usr/share/applications/pulseusb.desktop"
	appIcon     = "/usr/share/icons/hicolor/128x128/apps/pulseusb.png"
	appUnitText = `[Unit]
Description=PulseUSB (uses USB devices shared by PulseUSB servers)
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=` + appBin + ` -service -config ` + appConfig + `
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`
	appDesktopText = `[Desktop Entry]
Type=Application
Name=PulseUSB
Comment=Use USB devices plugged into another computer
Exec=` + appBin + `
Icon=pulseusb
Terminal=false
StartupWMClass=pulseusb
Categories=Utility;
`
)

type appInstaller struct {
	root string // prefix for every path written; empty on a real system
	out  io.Writer
	run  func(name string, args ...string) (string, error)
	have func(name string) bool
	// libsMissing reports whether the window's libraries are absent.
	libsMissing func() bool
}

// installPackages installs system packages with whichever package manager
// this distribution has. names maps a package manager to its package names.
func (i *appInstaller) installPackages(what string, names map[string][]string) bool {
	managers := []struct {
		cmd  string
		args []string
	}{
		{"pacman", []string{"-S", "--needed", "--noconfirm"}},
		{"apt-get", []string{"install", "-y"}},
		{"dnf", []string{"install", "-y"}},
		{"zypper", []string{"--non-interactive", "install"}},
	}
	for _, m := range managers {
		pkgs := names[m.cmd]
		if len(pkgs) == 0 || !i.have(m.cmd) {
			continue
		}
		fmt.Fprintf(i.out, "Installing %s (%s %s)...\n", what, m.cmd, strings.Join(pkgs, " "))
		if out, err := i.run(m.cmd, append(m.args, pkgs...)...); err != nil {
			fmt.Fprintf(i.out, "  that failed: %s\n", lastLine(out))
			return false
		}
		return true
	}
	return false
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (i *appInstaller) p(path string) string { return filepath.Join(i.root, path) }

func (i *appInstaller) write(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(i.p(path)), 0o755); err != nil {
		return err
	}
	os.Remove(i.p(path)) // a running program's file can be replaced, not rewritten
	return os.WriteFile(i.p(path), data, mode)
}

func (i *appInstaller) install() error {
	if i.root == "" && os.Geteuid() != 0 {
		return fmt.Errorf("installing needs root: run it with sudo")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if filepath.Clean(self) != i.p(appBin) {
		if err := i.write(appBin, data, 0o755); err != nil {
			return err
		}
	}
	// The kernel's virtual USB controller: now, and at every boot.
	if err := i.write(appModules, []byte("vhci-hcd\n"), 0o644); err != nil {
		return err
	}
	if out, err := i.run("modprobe", "vhci-hcd"); err != nil {
		// Debian and Ubuntu ship the module in a separate package for the
		// running kernel; other distributions include it with the kernel.
		kernel, _ := i.run("uname", "-r")
		kernel = strings.TrimSpace(kernel)
		if kernel != "" && i.installPackages("the kernel module for remote USB devices",
			map[string][]string{"apt-get": {"linux-modules-extra-" + kernel}}) {
			out, err = i.run("modprobe", "vhci-hcd")
		}
		if err != nil {
			fmt.Fprintf(i.out, "Warning: the vhci-hcd kernel module could not be loaded (%s).\n"+
				"Remote devices cannot be plugged in without it. Look for a package named\n"+
				"\"usbip\" or \"linux-modules-extra\" for your kernel.\n\n", lastLine(out))
		}
	}
	// The window's libraries.
	if i.libsMissing != nil && i.libsMissing() {
		ok := i.installPackages("the libraries for the PulseUSB window", map[string][]string{
			"pacman":  {"webkit2gtk-4.1"},
			"apt-get": {"libwebkit2gtk-4.1-0"},
			"dnf":     {"webkit2gtk4.1"},
			"zypper":  {"libwebkit2gtk-4_1-0"},
		})
		if !ok || i.libsMissing() {
			fmt.Fprintf(i.out, "Note: %s\n\n", windowLibsHint)
		}
	}
	for path, content := range map[string][]byte{
		appUnit: []byte(appUnitText), appDesktop: []byte(appDesktopText), appIcon: gui.IconPNG(),
	} {
		if err := i.write(path, content, 0o644); err != nil {
			return err
		}
	}
	if !i.have("systemctl") {
		fmt.Fprintf(i.out, "Installed, but this system has no systemd; start it yourself with:\n  sudo %s -service -config %s\n", appBin, appConfig)
		return nil
	}
	i.run("systemctl", "daemon-reload")
	if out, err := i.run("systemctl", "enable", "pulseusb"); err != nil {
		return fmt.Errorf("could not enable the service: %s", strings.TrimSpace(out))
	}
	if out, err := i.run("systemctl", "restart", "pulseusb"); err != nil {
		return fmt.Errorf("could not start the service: %s", strings.TrimSpace(out))
	}
	fmt.Fprintf(i.out, "PulseUSB is installed and running, and will start with this computer.\n"+
		"  Open it:    \"PulseUSB\" in the application menu, or run: pulseusb\n"+
		"  Its log:    journalctl -u pulseusb -f\n"+
		"  Remove it:  sudo pulseusb uninstall\n")
	return nil
}

func (i *appInstaller) uninstall() error {
	if i.root == "" && os.Geteuid() != 0 {
		return fmt.Errorf("uninstalling needs root: run it with sudo")
	}
	if i.have("systemctl") {
		i.run("systemctl", "disable", "--now", "pulseusb")
	}
	for _, f := range []string{appUnit, appDesktop, appIcon, appModules, appBin} {
		os.Remove(i.p(f))
	}
	if i.have("systemctl") {
		i.run("systemctl", "daemon-reload")
	}
	fmt.Fprintf(i.out, "PulseUSB removed. Your settings are kept in %s.\n", filepath.Dir(appConfig))
	return nil
}

// platformCommand handles "install" and "uninstall".
func platformCommand(args []string) bool {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return false
	}
	i := &appInstaller{
		out: os.Stdout,
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		have:        func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		libsMissing: windowLibsMissing,
	}
	var err error
	if args[0] == "install" {
		err = i.install()
	} else {
		err = i.uninstall()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pulseusb:", err)
		os.Exit(1)
	}
	return true
}
