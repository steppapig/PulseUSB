// Package usbfs drives USB devices from user space through the kernel's
// usbfs interface (/dev/bus/usb). It needs no kernel module beyond the USB
// core, which is what lets the server run on minimal systems.
//go:build linux

package usbfs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"pulseusb/internal/desc"
	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

// struct usbdevfs_urb, without the trailing iso_frame_desc[] array.
type urbHdr struct {
	typ             uint8
	endpoint        uint8
	status          int32
	flags           uint32
	buffer          uintptr
	bufferLength    int32
	actualLength    int32
	startFrame      int32
	numberOfPackets int32
	errorCount      int32
	signr           uint32
	usercontext     uintptr
}

// struct usbdevfs_iso_packet_desc
type isoDesc struct {
	length, actualLength, status uint32
}

type setInterface struct{ iface, alt uint32 }

type ioctlReq struct {
	ifno, code int32
	data       uintptr
}

type disconnectClaim struct {
	iface, flags uint32
	driver       [256]byte
}

func ioc(dir, nr, size uintptr) uintptr {
	return dir<<iocDirShift | size<<16 | 'U'<<8 | nr
}

var (
	ioSetInterface    = ioc(iocRead, 4, unsafe.Sizeof(setInterface{}))
	ioSetConfig       = ioc(iocRead, 5, 4)
	ioSubmitURB       = ioc(iocRead, 10, unsafe.Sizeof(urbHdr{}))
	ioDiscardURB      = ioc(iocNone, 11, 0)
	ioReapURBNDelay   = ioc(iocWrite, 13, unsafe.Sizeof(uintptr(0)))
	ioClaimInterface  = ioc(iocRead, 15, 4)
	ioReleaseIface    = ioc(iocRead, 16, 4)
	ioIoctl           = ioc(iocRead|iocWrite, 18, unsafe.Sizeof(ioctlReq{}))
	ioReset           = ioc(iocNone, 20, 0)
	ioClearHalt       = ioc(iocRead, 21, 4)
	ioDisconnect      = ioc(iocNone, 22, 0)
	ioConnect         = ioc(iocNone, 23, 0)
	ioDisconnectClaim = ioc(iocRead, 27, unsafe.Sizeof(disconnectClaim{}))
)

// usbfs URB types and flags.
const (
	urbIso       = 0
	urbInterrupt = 1
	urbControl   = 2
	urbBulk      = 3

	urbShortNotOK = 0x01
	urbIsoASAP    = 0x02
	urbZeroPacket = 0x40
)

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == 0 {
			return nil
		}
		if e != syscall.EINTR {
			return e
		}
	}
}

// Backend exports the devices found under sysfs.
type Backend struct {
	// Match decides whether a device may be exported.
	Match func(d *proto.Device) bool

	SysRoot string // default /sys/bus/usb/devices
	DevRoot string // default /dev/bus/usb

	mu sync.Mutex
	// busy holds the devices a client has attached, with what was known
	// about each when it was handed over. While a device is in use the
	// server answers questions about it from here and leaves the device
	// itself alone, so listing (which clients do every couple of seconds)
	// can never disturb a live audio stream or input device.
	busy map[string]*inUse
	// kinds remembers what each device is, read while the device was still
	// with this machine (the description is not readable while in use).
	kinds   map[string]string
	hidInfo map[string]map[uint8]usb.HIDEndpoint
}

var busIDRe = regexp.MustCompile(`^\d+-\d+(\.\d+)*$`)

func (b *Backend) sysRoot() string {
	if b.SysRoot != "" {
		return b.SysRoot
	}
	return "/sys/bus/usb/devices"
}

func readAttr(dir, name string) string {
	data, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(data))
}

func speedOf(s string) uint32 {
	switch s {
	case "1.5":
		return proto.SpeedLow
	case "12":
		return proto.SpeedFull
	case "480":
		return proto.SpeedHigh
	case "5000":
		return proto.SpeedSuper
	case "10000", "20000":
		return proto.SpeedSuperPlus
	}
	return proto.SpeedHigh
}

// describe reads one device from sysfs.
func (b *Backend) describe(busid string) (*proto.Device, *desc.Config, error) {
	dir := filepath.Join(b.sysRoot(), busid)
	blob, err := os.ReadFile(filepath.Join(dir, "descriptors"))
	if err != nil {
		return nil, nil, err
	}
	active, _ := strconv.Atoi(readAttr(dir, "bConfigurationValue"))
	dev, cfg, err := desc.Parse(blob, uint8(active))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", busid, err)
	}
	bus, _ := strconv.Atoi(readAttr(dir, "busnum"))
	num, _ := strconv.Atoi(readAttr(dir, "devnum"))
	dev.BusID = busid
	dev.BusNum, dev.DevNum = uint32(bus), uint32(num)
	dev.Speed = speedOf(readAttr(dir, "speed"))
	if p, err := filepath.EvalSymlinks(dir); err == nil {
		dev.Path = p
	} else {
		dev.Path = dir
	}
	return dev, cfg, nil
}

type inUse struct {
	info *proto.Device
	name string
}

// attached returns the remembered details of a device in use, if it is.
func (b *Backend) attached(busid string) *inUse {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy[busid]
}

// List returns every exportable device (hubs are never exported).
func (b *Backend) List() ([]*proto.Device, error) {
	entries, err := os.ReadDir(b.sysRoot())
	if err != nil {
		return nil, err
	}
	var out []*proto.Device
	for _, e := range entries {
		if !busIDRe.MatchString(e.Name()) {
			continue
		}
		if u := b.attached(e.Name()); u != nil {
			out = append(out, u.info) // in use: do not touch the device
			continue
		}
		dev, _, err := b.describe(e.Name())
		if err != nil || dev.Class == desc.ClassHub || (b.Match != nil && !b.Match(dev)) {
			continue
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BusID < out[j].BusID })
	return out, nil
}

// Kinds says what each exportable device is, from the HID descriptions its
// interfaces give of themselves: "Game controller", "Keyboard", "Mouse"...
// Devices that are not keyboards, mice or controllers are left out.
func (b *Backend) Kinds() map[string]string {
	out := map[string]string{}
	devs, _ := b.List()
	for _, d := range devs {
		key := fmt.Sprintf("%s/%04x:%04x", d.BusID, d.VendorID, d.ProductID)
		if b.attached(d.BusID) == nil {
			kind := ""
			// e.g. 1-3:1.3/0003:054C:0CE6.0005/report_descriptor
			files, _ := filepath.Glob(filepath.Join(b.sysRoot(), d.BusID+":*", "*:*:*.*", "report_descriptor"))
			rank := map[string]int{"": 0, "Mouse": 1, "Keyboard": 2, "Keyboard and mouse": 3, "Game controller": 4}
			for _, f := range files {
				if rd, err := os.ReadFile(f); err == nil {
					k := desc.HIDKind(rd)
					if k == "Keyboard" && kind == "Mouse" || k == "Mouse" && kind == "Keyboard" {
						k = "Keyboard and mouse"
					}
					if rank[k] > rank[kind] {
						kind = k
					}
				}
			}
			b.mu.Lock()
			if b.kinds == nil {
				b.kinds = map[string]string{}
			}
			if kind != "" || len(files) > 0 {
				b.kinds[key] = kind
			}
			b.mu.Unlock()
		}
		b.mu.Lock()
		k := b.kinds[key]
		b.mu.Unlock()
		if k != "" {
			out[d.BusID] = k
		}
	}
	return out
}

// Names returns "manufacturer product" as reported by each exportable device.
func (b *Backend) Names() map[string]string {
	out := map[string]string{}
	devs, _ := b.List()
	for _, d := range devs {
		var name string
		if u := b.attached(d.BusID); u != nil {
			name = u.name // in use: do not touch the device
		} else {
			dir := filepath.Join(b.sysRoot(), d.BusID)
			name = strings.TrimSpace(readAttr(dir, "manufacturer") + " " + readAttr(dir, "product"))
		}
		if name != "" {
			out[d.BusID] = name
		}
	}
	return out
}

// Open takes the device away from its host drivers and hands it to a client.
func (b *Backend) Open(busid string) (usb.Device, *proto.Device, error) {
	if !busIDRe.MatchString(busid) {
		return nil, nil, usb.ErrNotFound
	}
	if b.attached(busid) != nil {
		return nil, nil, usb.ErrBusy // and leave the device in use alone
	}
	info, cfg, err := b.describe(busid)
	if err != nil || info.Class == desc.ClassHub || (b.Match != nil && !b.Match(info)) {
		return nil, nil, usb.ErrNotFound
	}
	b.mu.Lock()
	if b.busy[busid] != nil {
		b.mu.Unlock()
		return nil, nil, usb.ErrBusy
	}
	if b.busy == nil {
		b.busy = map[string]*inUse{}
	}
	dir := filepath.Join(b.sysRoot(), busid)
	b.busy[busid] = &inUse{info: info, name: strings.TrimSpace(readAttr(dir, "manufacturer") + " " + readAttr(dir, "product"))}
	b.mu.Unlock()
	release := func() {
		b.mu.Lock()
		delete(b.busy, busid)
		b.mu.Unlock()
	}

	root := b.DevRoot
	if root == "" {
		root = "/dev/bus/usb"
	}
	node := fmt.Sprintf("%s/%03d/%03d", root, info.BusNum, info.DevNum)
	fd, err := syscall.Open(node, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("open %s: %w", node, err)
	}
	d := &Device{
		fd:       fd,
		hid:      b.hidInterfaces(busid, info),
		cfg:      cfg,
		describe: func() (*proto.Device, *desc.Config, error) { return b.describe(busid) },
		release:  release,
		pending:  map[uintptr]*urb{},
		byXfer:   map[*usb.Transfer]*urb{},
		reapDone: make(chan struct{}),
	}
	if err := d.start(); err != nil {
		d.Close()
		return nil, nil, fmt.Errorf("%s: %w", busid, err)
	}
	return d, info, nil
}

// urb is one request in flight inside the kernel. mem and data must stay
// referenced (and are never moved by Go's collector) until it is reaped.
type urb struct {
	mem  []uint64 // backing store of the usbdevfs_urb
	hdr  *urbHdr
	data []byte // buffer handed to the kernel
	t    *usb.Transfer
	done func(*usb.Transfer)
}

// Device is a USB device opened through usbfs.
type Device struct {
	fd       int
	hid      map[uint8]usb.HIDEndpoint // interface number -> what its reports are
	describe func() (*proto.Device, *desc.Config, error)
	release  func()

	cfgMu sync.Mutex
	cfg   *desc.Config

	mu      sync.Mutex
	pending map[uintptr]*urb
	byXfer  map[*usb.Transfer]*urb

	epfd     int
	wake     [2]int
	started  bool
	reapDone chan struct{}
	close    sync.Once
}

func (d *Device) start() error {
	if err := d.claimAll(); err != nil {
		return err
	}
	var err error
	if d.epfd, err = syscall.EpollCreate1(syscall.EPOLL_CLOEXEC); err != nil {
		return err
	}
	if err = syscall.Pipe2(d.wake[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		syscall.Close(d.epfd)
		return err
	}
	// usbfs reports EPOLLOUT when a URB can be reaped, EPOLLHUP on unplug.
	ev := syscall.EpollEvent{Events: syscall.EPOLLOUT, Fd: int32(d.fd)}
	if err = syscall.EpollCtl(d.epfd, syscall.EPOLL_CTL_ADD, d.fd, &ev); err == nil {
		ev = syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(d.wake[0])}
		err = syscall.EpollCtl(d.epfd, syscall.EPOLL_CTL_ADD, d.wake[0], &ev)
	}
	d.started = true
	if err != nil {
		close(d.reapDone)
		return err
	}
	go d.reapLoop()
	return nil
}

func (d *Device) claimAll() error {
	for _, n := range d.cfg.Ifaces {
		dc := disconnectClaim{iface: uint32(n)}
		err := ioctl(d.fd, ioDisconnectClaim, unsafe.Pointer(&dc))
		if err == syscall.ENOTTY { // kernels before 3.7
			r := ioctlReq{ifno: int32(n), code: int32(ioDisconnect)}
			ioctl(d.fd, ioIoctl, unsafe.Pointer(&r))
			v := uint32(n)
			err = ioctl(d.fd, ioClaimInterface, unsafe.Pointer(&v))
		}
		if err != nil {
			return fmt.Errorf("claim interface %d: %w", n, err)
		}
	}
	return nil
}

func (d *Device) releaseAll(reconnect bool) {
	for _, n := range d.cfg.Ifaces {
		v := uint32(n)
		ioctl(d.fd, ioReleaseIface, unsafe.Pointer(&v))
	}
	if reconnect {
		for _, n := range d.cfg.Ifaces {
			r := ioctlReq{ifno: int32(n), code: int32(ioConnect)}
			ioctl(d.fd, ioIoctl, unsafe.Pointer(&r))
		}
	}
}

// special handles the standard requests that must go through the host's USB
// core instead of being sent as raw control transfers, so the kernel's view
// of the device stays in sync. It reports whether it handled the request.
func (d *Device) special(t *usb.Transfer) (bool, error) {
	s := t.Setup
	wValue := uint16(s[2]) | uint16(s[3])<<8
	wIndex := uint16(s[4]) | uint16(s[5])<<8
	switch {
	case s[0] == 0x00 && s[1] == 9: // SET_CONFIGURATION
		d.cfgMu.Lock()
		defer d.cfgMu.Unlock()
		d.releaseAll(false)
		v := uint32(wValue)
		err := ioctl(d.fd, ioSetConfig, unsafe.Pointer(&v))
		if _, cfg, derr := d.describe(); derr == nil {
			d.cfg = cfg
		}
		if cerr := d.claimAll(); err == nil {
			err = cerr
		}
		return true, err
	case s[0] == 0x01 && s[1] == 11: // SET_INTERFACE
		v := setInterface{iface: uint32(wIndex), alt: uint32(wValue)}
		return true, ioctl(d.fd, ioSetInterface, unsafe.Pointer(&v))
	case s[0] == 0x02 && s[1] == 1 && wValue == 0: // CLEAR_FEATURE(ENDPOINT_HALT)
		v := uint32(wIndex & 0xff)
		return true, ioctl(d.fd, ioClearHalt, unsafe.Pointer(&v))
	case s[0] == 0x23 && s[1] == 3 && wValue == 4: // SET_FEATURE(PORT_RESET)
		return true, ioctl(d.fd, ioReset, nil)
	}
	return false, nil
}

// Submit implements usb.Device.
func (d *Device) Submit(t *usb.Transfer, done func(*usb.Transfer)) error {
	u := &urb{t: t, done: done}
	n := unsafe.Sizeof(urbHdr{}) + uintptr(len(t.Iso))*unsafe.Sizeof(isoDesc{})
	u.mem = make([]uint64, (n+7)/8)
	h := (*urbHdr)(unsafe.Pointer(&u.mem[0]))
	u.hdr = h
	h.endpoint = t.Endpoint
	if t.In {
		h.endpoint |= 0x80
	}

	if t.Endpoint == 0 {
		handled, err := d.special(t)
		if err != nil {
			return err
		}
		if handled {
			t.Status, t.ActualLength = 0, 0
			done(t)
			return nil
		}
		h.typ = urbControl
		h.endpoint = 0
		u.data = make([]byte, 8+len(t.Buffer))
		copy(u.data, t.Setup[:])
		if !t.In {
			copy(u.data[8:], t.Buffer)
		}
	} else {
		d.cfgMu.Lock()
		typ, ok := d.cfg.Endpoints[h.endpoint]
		d.cfgMu.Unlock()
		if !ok {
			return syscall.EINVAL
		}
		u.data = t.Buffer
		switch typ {
		case desc.Bulk:
			h.typ = urbBulk
			if !t.In && t.Flags&usb.FlagZeroPacket != 0 {
				h.flags |= urbZeroPacket
			}
		case desc.Interrupt:
			h.typ = urbInterrupt
		case desc.Iso:
			if len(t.Iso) == 0 {
				return syscall.EINVAL
			}
			h.typ = urbIso
			h.flags = urbIsoASAP
			h.numberOfPackets = int32(len(t.Iso))
			descs := u.isoDescs()
			// usbfs wants the packets back to back in the buffer.
			total, packed := uint32(0), true
			for i, p := range t.Iso {
				if p.Offset != total {
					packed = false
				}
				descs[i].length = p.Length
				total += p.Length
			}
			if !packed || int(total) != len(t.Buffer) {
				u.data = make([]byte, total)
				if !t.In {
					off := uint32(0)
					for _, p := range t.Iso {
						copy(u.data[off:off+p.Length], t.Buffer[p.Offset:])
						off += p.Length
					}
				}
			}
		default:
			return syscall.EINVAL
		}
		if t.In && typ != desc.Iso && t.Flags&usb.FlagShortNotOK != 0 {
			h.flags |= urbShortNotOK
		}
	}
	if len(u.data) > 0 {
		h.buffer = uintptr(unsafe.Pointer(&u.data[0]))
	}
	h.bufferLength = int32(len(u.data))

	key := uintptr(unsafe.Pointer(h))
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ioctl(d.fd, ioSubmitURB, unsafe.Pointer(h)); err != nil {
		return err
	}
	d.pending[key] = u
	d.byXfer[t] = u
	return nil
}

func (u *urb) isoDescs() []isoDesc {
	p := unsafe.Add(unsafe.Pointer(u.hdr), unsafe.Sizeof(urbHdr{}))
	return unsafe.Slice((*isoDesc)(p), len(u.t.Iso))
}

// hidInterfaces reads, while the device is still with this machine, what
// the HID interfaces' reports look like (remembered for later opens, in case
// the descriptions cannot be read then).
func (b *Backend) hidInterfaces(busid string, info *proto.Device) map[uint8]usb.HIDEndpoint {
	key := fmt.Sprintf("%s/%04x:%04x", busid, info.VendorID, info.ProductID)
	out := map[uint8]usb.HIDEndpoint{}
	files, _ := filepath.Glob(filepath.Join(b.sysRoot(), busid+":*", "*:*:*.*", "report_descriptor"))
	for _, f := range files {
		iface := filepath.Base(filepath.Dir(filepath.Dir(f))) // e.g. 1-3:1.2
		dot := strings.LastIndexByte(iface, '.')
		n, err := strconv.Atoi(iface[dot+1:])
		rd, err2 := os.ReadFile(f)
		if dot < 0 || err != nil || err2 != nil {
			continue
		}
		rel, ids := desc.HIDRelease(rd)
		out[uint8(n)] = usb.HIDEndpoint{Releasable: rel, ReportIDs: ids}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hidInfo == nil {
		b.hidInfo = map[string]map[uint8]usb.HIDEndpoint{}
	}
	if len(out) > 0 {
		b.hidInfo[key] = out
	} else if old, ok := b.hidInfo[key]; ok {
		out = old
	}
	return out
}

// HIDEndpoints returns the HID interfaces' IN endpoints and what their
// reports look like.
func (d *Device) HIDEndpoints() map[uint8]usb.HIDEndpoint {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	eps := map[uint8]usb.HIDEndpoint{}
	for addr, n := range d.cfg.EndpointIface {
		if h, ok := d.hid[n]; ok && addr&0x80 != 0 && d.cfg.Endpoints[addr] == desc.Interrupt {
			eps[addr&0x0f] = h
		}
	}
	return eps
}

// KeyboardEndpoints returns the IN endpoints of the device's keyboard
// interfaces (HID boot keyboards), whose reports the server watches for
// the escape keys.
func (d *Device) KeyboardEndpoints() map[uint8]bool {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	kbd := map[uint8]bool{}
	for i, n := range d.cfg.Ifaces {
		if i < len(d.cfg.IfaceInfo) && d.cfg.IfaceInfo[i] == (proto.Interface{Class: 3, SubClass: 1, Protocol: 1}) {
			kbd[n] = true
		}
	}
	eps := map[uint8]bool{}
	for addr, n := range d.cfg.EndpointIface {
		if addr&0x80 != 0 && kbd[n] {
			eps[addr&0x0f] = true
		}
	}
	return eps
}

// Cancel implements usb.Device. The URB completes through the reaper.
func (d *Device) Cancel(t *usb.Transfer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u := d.byXfer[t]; u != nil {
		ioctl(d.fd, ioDiscardURB, unsafe.Pointer(u.hdr))
	}
}

func (d *Device) reapLoop() {
	defer close(d.reapDone)
	events := make([]syscall.EpollEvent, 2)
	for {
		n, err := syscall.EpollWait(d.epfd, events, -1)
		if err == syscall.EINTR {
			continue
		}
		gone := err != nil
		for _, ev := range events[:max(n, 0)] {
			if ev.Fd == int32(d.wake[0]) {
				return // Close was called
			}
			if ev.Events&(syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
				gone = true
			}
		}
		for {
			var p uintptr
			err := ioctl(d.fd, ioReapURBNDelay, unsafe.Pointer(&p))
			if err == syscall.EAGAIN {
				break
			}
			if err != nil {
				gone = true
				break
			}
			d.complete(p)
		}
		if gone {
			d.failAll()
			return
		}
	}
}

func (d *Device) complete(key uintptr) {
	d.mu.Lock()
	u := d.pending[key]
	delete(d.pending, key)
	if u != nil {
		delete(d.byXfer, u.t)
	}
	d.mu.Unlock()
	if u == nil {
		return
	}
	t, h := u.t, u.hdr
	t.Status = usb.StatusFromErrno(syscall.Errno(-h.status))
	t.ActualLength = h.actualLength
	t.ErrorCount = h.errorCount
	t.StartFrame = h.startFrame
	switch {
	case h.typ == urbControl:
		if t.In && h.actualLength > 0 {
			copy(t.Buffer, u.data[8:8+int(h.actualLength)])
		}
	case h.typ == urbIso:
		descs := u.isoDescs()
		separate := len(u.data) > 0 && len(t.Buffer) > 0 && &u.data[0] != &t.Buffer[0]
		off := uint32(0)
		for i := range t.Iso {
			p := &t.Iso[i]
			p.ActualLength = descs[i].actualLength
			p.Status = usb.StatusFromErrno(syscall.Errno(-int32(descs[i].status)))
			if t.In && separate {
				copy(t.Buffer[p.Offset:p.Offset+p.Length], u.data[off:off+p.Length])
			}
			off += p.Length
		}
	}
	u.done(t)
}

// failAll completes everything still pending after the device vanished.
func (d *Device) failAll() {
	d.mu.Lock()
	all := d.pending
	d.pending = map[uintptr]*urb{}
	d.byXfer = map[*usb.Transfer]*urb{}
	d.mu.Unlock()
	for _, u := range all {
		u.t.Status, u.t.ActualLength = -usb.ENODEV, 0
		u.done(u.t)
	}
}

// Close implements usb.Device: stops I/O, releases the interfaces and lets
// the host's own drivers bind to the device again.
func (d *Device) Close() error {
	d.close.Do(func() {
		if d.started {
			syscall.Write(d.wake[1], []byte{1})
			<-d.reapDone
		}
		d.mu.Lock()
		for _, u := range d.pending {
			ioctl(d.fd, ioDiscardURB, unsafe.Pointer(u.hdr))
		}
		d.mu.Unlock()
		d.cfgMu.Lock()
		d.releaseAll(true)
		d.cfgMu.Unlock()
		// Closing the descriptor makes the kernel drop any remaining URBs,
		// after which it no longer touches their memory.
		syscall.Close(d.fd)
		if d.started {
			syscall.Close(d.epfd)
			syscall.Close(d.wake[0])
			syscall.Close(d.wake[1])
		}
		d.release()
	})
	return nil
}
