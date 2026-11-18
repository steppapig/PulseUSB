package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
	"pulseusb/internal/usbids"
)

// installer sets the server up as a system service. root, run and output
// are replaceable so the steps can be tested without touching a real system.
type installer struct {
	root    string // prefix for every path written; empty on a real system
	in      *bufio.Reader
	out     io.Writer
	backend usb.Backend
	// run executes a command and returns its combined output.
	run func(name string, args ...string) (string, error)
	// have reports whether a command exists.
	have func(name string) bool
	yes  bool // take every default without asking
}

func realInstaller(b usb.Backend) *installer {
	return &installer{
		in: bufio.NewReader(os.Stdin), out: os.Stdout, backend: b,
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		have: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
	}
}

const (
	binPath   = "/usr/local/bin/pulseusb-server"
	unitPath  = "/etc/systemd/system/pulseusb-server.service"
	lidPath   = "/etc/systemd/logind.conf.d/pulseusb-lid.conf"
	logiPath  = "/etc/modprobe.d/pulseusb-logitech.conf"
	oldUnit   = "/etc/systemd/system/usbeth-server.service"
	oldBin    = "/usr/local/bin/usbeth-server"
	serverTCP = proto.DefaultPort
)

func (i *installer) p(path string) string { return filepath.Join(i.root, path) }

func (i *installer) say(format string, args ...any) { fmt.Fprintf(i.out, format+"\n", args...) }

// ask prints a question and returns the answer, or def for an empty one.
func (i *installer) ask(question, def string) string {
	if i.yes {
		return def
	}
	fmt.Fprintf(i.out, "%s [%s]: ", question, def)
	line, _ := i.in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func (i *installer) confirm(question string) bool {
	a := strings.ToLower(i.ask(question+" (y/n)", "y"))
	return a == "y" || a == "yes"
}

func (i *installer) write(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(i.p(path)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(i.p(path), []byte(content), mode)
}

func (i *installer) exists(path string) bool {
	_, err := os.Stat(i.p(path))
	return err == nil
}

// cleanName turns what the user typed into a name safe to show to clients
// and to put on a command line.
func cleanName(s string) string {
	var b strings.Builder
	for _, r := range strings.Join(strings.Fields(s), "-") {
		if r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

// chooseDevices asks which of the plugged-in devices to share and returns
// the server options for that choice.
func (i *installer) chooseDevices(preset []string, all bool) ([]string, []*proto.Device) {
	devs, _ := i.backend.List()
	if all {
		return []string{"-all"}, devs
	}
	if len(preset) > 0 {
		var opts []string
		for _, d := range preset {
			opts = append(opts, "-d", d)
		}
		return opts, devs
	}
	if len(devs) == 0 {
		i.say("No USB devices are plugged in right now, so every device plugged in later will be shared.")
		return []string{"-all"}, nil
	}
	names := map[string]string{}
	if n, ok := i.backend.(usb.Namer); ok {
		names = n.Names()
	}
	i.say("\nUSB devices on this machine:")
	for n, d := range devs {
		name := usbids.Describe(names[d.BusID], d.VendorID, d.ProductID)
		if name == "" {
			name = "unknown device"
		}
		i.say("  %d) %04x:%04x  %s", n+1, d.VendorID, d.ProductID, name)
	}
	i.say("Sharing a device hands it to the other PC, so leave out anything this machine needs\n" +
		"itself (its own keyboard, a USB network adapter, built-in devices).")
	answer := i.ask("Devices to share: numbers separated by spaces, or \"all\" for these and any plugged in later", "all")
	if strings.EqualFold(answer, "all") {
		return []string{"-all"}, devs
	}
	var opts []string
	var chosen []*proto.Device
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(answer, func(r rune) bool { return r == ' ' || r == ',' }) {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(devs) {
			i.say("  (ignoring %q)", f)
			continue
		}
		d := devs[n-1]
		id := fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)
		if !seen[id] {
			seen[id] = true
			opts = append(opts, "-d", id)
			chosen = append(chosen, d)
		}
	}
	if len(opts) == 0 {
		i.say("Nothing chosen; sharing all devices.")
		return []string{"-all"}, devs
	}
	return opts, chosen
}

func (i *installer) install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	name := fs.String("name", "", "server name shown to clients")
	all := fs.Bool("all", false, "share every USB device except hubs")
	var devices list
	fs.Var(&devices, "d", "device to share (vendor:product or bus id); repeatable")
	fs.BoolVar(&i.yes, "yes", false, "do not ask anything; use defaults for what is not given")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if i.root == "" && os.Geteuid() != 0 {
		return fmt.Errorf("installing needs root: run it with sudo")
	}
	i.say("PulseUSB server setup\n")

	// 1. Server name.
	host, _ := os.Hostname()
	if *name == "" {
		*name = i.ask("Name for this server, as the PulseUSB app will show it", host)
	}
	if *name = cleanName(*name); *name == "" {
		*name = cleanName(host)
	}

	// 2. Devices.
	opts, shared := i.chooseDevices(devices, *all)

	// 3. The program itself.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(self); err != nil {
		return err
	} else if filepath.Clean(self) != i.p(binPath) {
		if i.have("systemctl") {
			i.run("systemctl", "stop", "pulseusb-server") // the file cannot be replaced while it runs
		}
		os.Remove(i.p(binPath))
		if err := i.write(binPath, string(data), 0o755); err != nil {
			return err
		}
	}

	// 4. A service from when the program was called usbeth.
	if i.exists(oldUnit) {
		i.say("Replacing the old usbeth-server service.")
		i.run("systemctl", "disable", "--now", "usbeth-server")
		os.Remove(i.p(oldUnit))
		os.Remove(i.p(oldBin))
	}

	// 5. The service.
	unit := `[Unit]
Description=PulseUSB server
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=` + binPath + ` -name "` + *name + `" ` + strings.Join(opts, " ") + `
Restart=always
RestartSec=2
Nice=-10

[Install]
WantedBy=multi-user.target
`
	if err := i.write(unitPath, unit, 0o644); err != nil {
		return err
	}

	// 6. Firewall, if one is running.
	switch {
	case i.have("firewall-cmd") && func() bool { _, err := i.run("firewall-cmd", "--state"); return err == nil }():
		i.say("Opening port %s in firewalld.", serverTCP)
		i.run("firewall-cmd", "--permanent", "--add-port="+serverTCP+"/tcp")
		i.run("firewall-cmd", "--permanent", "--add-port="+serverTCP+"/udp")
		i.run("firewall-cmd", "--reload")
	case i.have("ufw") && func() bool { out, _ := i.run("ufw", "status"); return strings.Contains(out, "Status: active") }():
		i.say("Opening port %s in ufw.", serverTCP)
		i.run("ufw", "allow", serverTCP+"/tcp")
		i.run("ufw", "allow", serverTCP+"/udp")
	}

	// 7. Laptops: keep running with the lid closed.
	reboot := false
	if lids, _ := filepath.Glob(i.p("/proc/acpi/button/lid/*")); len(lids) > 0 && !i.exists(lidPath) {
		if i.confirm("\nThis looks like a laptop. Keep it running when the lid is closed?") {
			i.write(lidPath, "[Login]\nHandleLidSwitch=ignore\nHandleLidSwitchExternalPower=ignore\nHandleLidSwitchDocked=ignore\n", 0o644)
			reboot = true
		}
	}

	// 8. Logitech wireless receivers: this machine's own driver switches
	// them into a mode Windows cannot read, and they stay in it.
	logitech := len(opts) == 1 && opts[0] == "-all"
	for _, d := range shared {
		logitech = logitech || d.VendorID == 0x046d
	}
	for _, o := range opts {
		logitech = logitech || strings.HasPrefix(strings.ToLower(o), "046d:")
	}
	if logitech && i.exists("/sys/module/hid_logitech_dj") && !i.exists(logiPath) {
		if i.confirm("\nLogitech wireless receivers only work on the other PC if this machine's own\n" +
			"Logitech receiver driver is switched off. Switch it off?") {
			i.write(logiPath, "# Added by PulseUSB: keep Logitech receivers in the mode Windows understands.\nblacklist hid_logitech_dj\n", 0o644)
			if i.have("update-initramfs") {
				i.run("update-initramfs", "-u")
			}
			reboot = true
		}
	}

	// 9. Start.
	if !i.have("systemctl") {
		i.say("\nInstalled, but this system has no systemd; start the server yourself with:\n  %s -name \"%s\" %s",
			binPath, *name, strings.Join(opts, " "))
		return nil
	}
	i.run("systemctl", "daemon-reload")
	if out, err := i.run("systemctl", "enable", "--now", "pulseusb-server"); err != nil {
		return fmt.Errorf("could not start the service: %s", strings.TrimSpace(out))
	}
	i.say("\nDone. \"%s\" is running and will start with this machine.", *name)
	i.say("  Change the setup:  sudo pulseusb-server install")
	i.say("  Watch its log:     journalctl -u pulseusb-server -f")
	i.say("  Remove it:         sudo pulseusb-server uninstall")
	i.say("  Use a device here: pulseusb-server release <device|all>   (back to sharing: pulseusb-server share)")
	i.say("\nKeyboard and mouse stuck on the other computer? Press Ctrl+Alt+Shift+F12 on that")
	i.say("keyboard to bring every device back to this machine; press it again here to send")
	i.say("them back. (To change the keys, add -escape to the ExecStart line in %s.)", unitPath)
	if reboot {
		i.say("\nReboot once for the lid or Logitech change to take effect, and re-plug any Logitech receiver.")
	}
	return nil
}

func (i *installer) uninstall() error {
	if i.root == "" && os.Geteuid() != 0 {
		return fmt.Errorf("uninstalling needs root: run it with sudo")
	}
	if i.have("systemctl") {
		i.run("systemctl", "disable", "--now", "pulseusb-server")
	}
	for _, f := range []string{unitPath, lidPath, logiPath, binPath} {
		os.Remove(i.p(f))
	}
	if i.have("systemctl") {
		i.run("systemctl", "daemon-reload")
	}
	i.say("PulseUSB server removed. Firewall rules for port %s, if any were added, are left in place.", serverTCP)
	return nil
}
