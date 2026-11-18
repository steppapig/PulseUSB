// Package vhci talks to the Linux kernel's virtual USB host controller
// (vhci-hcd), which makes an imported device appear as locally plugged in.
package vhci

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pulseusb/internal/proto"
)

// Port states reported by the kernel.
const (
	StateFree = 4
	StateUsed = 6
)

// Controller is the sysfs directory of vhci-hcd.
type Controller struct {
	Dir      string // default /sys/devices/platform/vhci_hcd.0
	StateDir string // default /var/run/vhci_hcd
}

func (c *Controller) dir() string {
	if c.Dir != "" {
		return c.Dir
	}
	return "/sys/devices/platform/vhci_hcd.0"
}

func (c *Controller) stateDir() string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return "/var/run/vhci_hcd"
}

// Port is one virtual root-hub port.
type Port struct {
	Hub    string // "hs" (USB 2) or "ss" (USB 3); empty on old kernels
	Number int
	State  int
	Speed  uint32
	DevID  uint32
	BusID  string // local bus id of the attached device
}

// Ports lists every virtual port.
func (c *Controller) Ports() ([]Port, error) {
	files, _ := filepath.Glob(filepath.Join(c.dir(), "status*"))
	if len(files) == 0 {
		return nil, fmt.Errorf("vhci-hcd not available at %s (try: modprobe vhci-hcd)", c.dir())
	}
	sort.Strings(files)
	var ports []Port
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			if p, ok := parseLine(sc.Text()); ok {
				ports = append(ports, p)
			}
		}
		fh.Close()
	}
	return ports, nil
}

// parseLine understands both status formats:
//
//	hub port sta spd dev      sockfd local_busid   (Linux >= 4.13)
//	prt sta spd dev socket local_busid            (older)
func parseLine(line string) (Port, bool) {
	f := strings.Fields(line)
	var p Port
	switch len(f) {
	case 7:
		p.Hub, f = f[0], f[1:]
	case 6:
	default:
		return p, false
	}
	n, err1 := strconv.Atoi(f[0])
	st, err2 := strconv.Atoi(f[1])
	spd, err3 := strconv.Atoi(f[2])
	dev, err4 := strconv.ParseUint(f[3], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return p, false // header line
	}
	p.Number, p.State, p.Speed, p.DevID, p.BusID = n, st, uint32(spd), uint32(dev), f[5]
	return p, true
}

// Attach hands a connected socket to the kernel and returns the port used.
func (c *Controller) Attach(sockfd int, devid, speed uint32) (int, error) {
	ports, err := c.Ports()
	if err != nil {
		return -1, err
	}
	want := "hs"
	if speed >= proto.SpeedSuper {
		want = "ss"
	}
	err = errors.New("no free virtual USB port for this device speed")
	for _, p := range ports {
		if p.State != StateFree || (p.Hub != "" && p.Hub != want) {
			continue
		}
		cmd := fmt.Sprintf("%d %d %d %d", p.Number, sockfd, devid, speed)
		if err = os.WriteFile(filepath.Join(c.dir(), "attach"), []byte(cmd), 0); err == nil {
			return p.Number, nil
		}
	}
	return -1, fmt.Errorf("attach: %w", err)
}

// Detach unplugs the device on a virtual port.
func (c *Controller) Detach(port int) error {
	os.Remove(c.recordPath(port))
	return os.WriteFile(filepath.Join(c.dir(), "detach"), []byte(strconv.Itoa(port)), 0)
}

func (c *Controller) recordPath(port int) string {
	return filepath.Join(c.stateDir(), fmt.Sprintf("port%d", port))
}

// Record notes where a port's device came from, in the format the stock
// usbip tool uses, so "usbip port" understands our attachments too.
func (c *Controller) Record(port int, host, tcpPort, busid string) {
	os.MkdirAll(c.stateDir(), 0o700)
	os.WriteFile(c.recordPath(port), []byte(fmt.Sprintf("%s %s %s\n", host, tcpPort, busid)), 0o600)
}

// Origin returns what Record stored for a port, if anything.
func (c *Controller) Origin(port int) string {
	data, _ := os.ReadFile(c.recordPath(port))
	return strings.TrimSpace(string(data))
}
