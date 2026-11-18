package attach

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pulseusb/internal/client"
)

// On Windows a remote device can only be made to look locally plugged in by
// a kernel driver. We use the signed open-source usbip-win2 driver and drive
// it through its own command-line tool, whose options are stable across
// releases (the driver's private interface is not).
const Note = `Needs usbip-win2 installed (https://github.com/vadimgrn/usbip-win2/releases).
Set USBIP_EXE if usbip.exe is not on PATH or in its default folder.
Video and storage devices are attached in the driver's zero-copy receive
mode, everything else in low-latency mode; PULSEUSB_RECEIVE_MODE (zero-copy
or low-latency) forces one mode for all devices.`

func usbipExe() (string, error) {
	if p := os.Getenv("USBIP_EXE"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("usbip.exe"); err == nil {
		return p, nil
	}
	for _, dir := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "USBip", "usbip.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("usbip.exe not found: install usbip-win2 or set USBIP_EXE")
}

// DriverInstalled reports whether usbip-win2's tool can be found.
func DriverInstalled() bool {
	_, err := usbipExe()
	return err == nil
}

func run(args ...string) (string, error) {
	exe, err := usbipExe()
	if err != nil {
		return "", err
	}
	// The driver tool normally answers at once; never let a hung call
	// freeze the app's handling of every other device.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	// Never flash a console window when called from the tray app.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("usbip.exe: %s", text)
	}
	return text, nil
}

// Attach asks the driver to connect to the server itself. Unless once is
// set, the driver keeps retrying on its own if the server is unreachable.
func Attach(host, busid string, once, streaming bool) error {
	h, p, _ := net.SplitHostPort(client.Addr(host))
	mode := os.Getenv("PULSEUSB_RECEIVE_MODE")
	if mode == "" {
		mode = "low-latency"
		if streaming {
			mode = "zero-copy"
		}
	}
	args := []string{"--tcp-port", p, "attach", "--remote", h, "--bus-id", busid, "--receive-mode", mode}
	if once {
		args = append(args, "--once")
	}
	_, err := run(args...)
	return err
}

// Attached returns the local port of each remote device currently plugged in.
func Attached() (map[string]int, error) {
	out, err := run("port")
	if err != nil {
		return nil, err
	}
	return parsePorts(out), nil
}

// PrintPorts lists attached devices on standard output.
func PrintPorts() error {
	out, err := run("port")
	if err != nil {
		return err
	}
	if out == "" {
		out = "no devices attached"
	}
	fmt.Println(out)
	return nil
}

// Detach unplugs the device on a local port.
func Detach(port int) error {
	_, err := run("detach", "--port", strconv.Itoa(port))
	return err
}
