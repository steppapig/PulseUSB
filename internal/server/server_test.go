package server

import (
	"bytes"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"pulseusb/internal/client"
	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

// fakeDev simulates a device: ep0 answers GET_DESCRIPTOR, ep1 IN is an
// interrupt endpoint that never completes on its own, ep2 OUT records bulk
// data, ep2 IN echoes it back, ep3 IN is isochronous.
type fakeDev struct {
	keyboard bool // ep1 is a keyboard endpoint
	nkro     bool // ep2 IN is a keyboard interface with numbered reports

	ep2report []byte // when set, ep2 IN answers once with it, then waits
	ep2sent   bool
	mu        sync.Mutex
	held      map[*usb.Transfer]func(*usb.Transfer)
	bulk      []byte
	closed    chan struct{}
}

var deviceDescriptor = []byte{18, 1, 0, 2, 0, 0, 0, 64, 0x34, 0x12, 0x78, 0x56, 0, 1, 1, 2, 3, 1}

func (f *fakeDev) Submit(t *usb.Transfer, done func(*usb.Transfer)) error {
	switch {
	case t.Endpoint == 0 && t.In:
		t.ActualLength = int32(copy(t.Buffer, deviceDescriptor))
		done(t)
	case t.Endpoint == 1 && t.In:
		f.mu.Lock()
		f.held[t] = done
		f.mu.Unlock()
	case t.Endpoint == 2 && !t.In:
		f.mu.Lock()
		f.bulk = append(f.bulk, t.Buffer...)
		f.mu.Unlock()
		t.ActualLength = int32(len(t.Buffer))
		go done(t)
	case t.Endpoint == 2 && t.In:
		f.mu.Lock()
		if f.ep2report != nil { // acting as a keyboard interface
			if f.ep2sent {
				f.held[t] = done
				f.mu.Unlock()
				return nil
			}
			f.ep2sent = true
			t.ActualLength = int32(copy(t.Buffer, f.ep2report))
			f.mu.Unlock()
			go done(t)
			return nil
		}
		t.ActualLength = int32(copy(t.Buffer, f.bulk))
		f.mu.Unlock()
		go done(t)
	case t.Endpoint == 3 && t.In:
		for i := range t.Iso {
			p := &t.Iso[i]
			p.ActualLength = p.Length / 2
			for j := uint32(0); j < p.ActualLength; j++ {
				t.Buffer[p.Offset+j] = byte(i + 1)
			}
		}
		go done(t)
	default:
		return syscall.EINVAL
	}
	return nil
}

func (f *fakeDev) Cancel(t *usb.Transfer) {
	f.mu.Lock()
	done := f.held[t]
	delete(f.held, t)
	f.mu.Unlock()
	if done != nil {
		t.Status = -usb.ENOENT
		go done(t)
	}
}

// HIDEndpoints: when nkro is set, ep2 IN is a numbered-report keyboard
// interface, as on many gaming keyboards.
func (f *fakeDev) HIDEndpoints() map[uint8]usb.HIDEndpoint {
	eps := map[uint8]usb.HIDEndpoint{}
	if f.keyboard {
		eps[1] = usb.HIDEndpoint{Releasable: true}
	}
	if f.nkro {
		eps[2] = usb.HIDEndpoint{Releasable: true, ReportIDs: true}
	}
	return eps
}

func (f *fakeDev) KeyboardEndpoints() map[uint8]bool {
	if f.keyboard {
		return map[uint8]bool{1: true}
	}
	return nil
}

// press completes the held ep1 transfers with a keyboard report.
func (f *fakeDev) press(report []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for t, done := range f.held {
		delete(f.held, t)
		t.ActualLength = int32(copy(t.Buffer, report))
		go done(t)
	}
}

func (f *fakeDev) Close() error { close(f.closed); return nil }

type fakeBackend struct {
	mu       sync.Mutex
	busy     bool
	dev      *fakeDev
	keyboard bool // devices opened from now on are keyboards
}

var fakeInfo = &proto.Device{
	Path: "/sys/devices/fake/1-1", BusID: "1-1", BusNum: 1, DevNum: 7, Speed: proto.SpeedHigh,
	VendorID: 0x1234, ProductID: 0x5678, BcdDevice: 0x0100, ConfigValue: 1, NumConfigs: 1,
	Interfaces: []proto.Interface{{Class: 3, SubClass: 1, Protocol: 2}, {Class: 8, SubClass: 6, Protocol: 80}},
}

func (b *fakeBackend) List() ([]*proto.Device, error) { return []*proto.Device{fakeInfo}, nil }
func (b *fakeBackend) Names() map[string]string       { return map[string]string{"1-1": "Acme Wheel Base"} }
func (b *fakeBackend) Kinds() map[string]string       { return map[string]string{"1-1": "Game controller"} }

func (b *fakeBackend) Open(busid string) (usb.Device, *proto.Device, error) {
	if busid != "1-1" {
		return nil, nil, usb.ErrNotFound
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy {
		return nil, nil, usb.ErrBusy
	}
	b.busy = true
	b.dev = &fakeDev{held: map[*usb.Transfer]func(*usb.Transfer){}, closed: make(chan struct{}), keyboard: b.keyboard, nkro: b.keyboard}
	return b.dev, fakeInfo, nil
}

func start(t *testing.T, allow ...*net.IPNet) (*fakeBackend, string, *Server) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{}
	s := &Server{Backend: b, Logf: t.Logf, Allow: allow}
	go s.Serve(l)
	t.Cleanup(func() { l.Close(); s.Shutdown() })
	return b, l.Addr().String(), s
}

func TestList(t *testing.T) {
	_, addr, _ := start(t)
	devs, err := client.List(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices", len(devs))
	}
	d := devs[0]
	if d.BusID != "1-1" || d.VendorID != 0x1234 || d.ProductID != 0x5678 || d.Path != fakeInfo.Path ||
		d.DevID() != 1<<16|7 || len(d.Interfaces) != 2 || d.Interfaces[1].Class != 8 {
		t.Fatalf("bad device %+v", d)
	}
}

// vhciSim plays the role of the client's kernel.
type vhciSim struct {
	t *testing.T
	c net.Conn
}

func (v *vhciSim) send(h proto.Header, data []byte, iso []proto.IsoDesc) {
	b := make([]byte, proto.HeaderSize)
	h.Marshal(b)
	b = append(b, data...)
	for i := range iso {
		b = iso[i].Append(b)
	}
	if _, err := v.c.Write(b); err != nil {
		v.t.Fatal(err)
	}
}

func (v *vhciSim) recv(in bool) (*proto.Header, []byte, []proto.IsoDesc) {
	v.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	h, err := proto.ReadHeader(v.c)
	if err != nil {
		v.t.Fatal(err)
	}
	var data []byte
	var iso []proto.IsoDesc
	if h.Command == proto.RetSubmit {
		if in {
			data = make([]byte, h.Length)
			if _, err := io.ReadFull(v.c, data); err != nil {
				v.t.Fatal(err)
			}
		}
		raw := make([]byte, int(h.NumPackets)*proto.IsoDescSize)
		if _, err := io.ReadFull(v.c, raw); err != nil {
			v.t.Fatal(err)
		}
		iso = make([]proto.IsoDesc, h.NumPackets)
		for i := range iso {
			iso[i].Unmarshal(raw[i*proto.IsoDescSize:])
		}
	}
	return h, data, iso
}

func attach(t *testing.T, addr string) *vhciSim {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	d, err := client.Import(c, "1-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.VendorID != 0x1234 || d.Speed != proto.SpeedHigh {
		t.Fatalf("bad import reply %+v", d)
	}
	return &vhciSim{t, c}
}

func TestTransfers(t *testing.T) {
	b, addr, _ := start(t)
	v := attach(t, addr)

	// Control IN: GET_DESCRIPTOR(device), asking for more than exists.
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 1, Direction: proto.DirIn, Ep: 0, Length: 64,
		Setup: [8]byte{0x80, 6, 0, 1, 0, 0, 64, 0}}, nil, nil)
	h, data, _ := v.recv(true)
	if h.Command != proto.RetSubmit || h.Seqnum != 1 || h.Status != 0 || !bytes.Equal(data, deviceDescriptor) {
		t.Fatalf("control: %+v %x", h, data)
	}

	// Bulk OUT then IN: every byte must come back unchanged.
	payload := make([]byte, 300000)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 2, Direction: proto.DirOut, Ep: 2, Length: int32(len(payload))}, payload, nil)
	h, _, _ = v.recv(false)
	if h.Seqnum != 2 || h.Status != 0 || int(h.Length) != len(payload) {
		t.Fatalf("bulk out: %+v", h)
	}
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 3, Direction: proto.DirIn, Ep: 2, Length: int32(len(payload))}, nil, nil)
	h, data, _ = v.recv(true)
	if h.Seqnum != 3 || !bytes.Equal(data, payload) {
		t.Fatalf("bulk in: %+v, %d bytes", h, len(data))
	}

	// Isochronous IN: 3 packets of 100 bytes, each half filled; the data
	// comes back packed, the descriptors keep their offsets.
	iso := []proto.IsoDesc{{Offset: 0, Length: 100}, {Offset: 100, Length: 100}, {Offset: 200, Length: 100}}
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 4, Direction: proto.DirIn, Ep: 3, Length: 300, NumPackets: 3}, nil, iso)
	h, data, riso := v.recv(true)
	if h.Seqnum != 4 || h.Length != 150 || h.NumPackets != 3 || len(data) != 150 {
		t.Fatalf("iso: %+v, %d bytes", h, len(data))
	}
	for i, p := range riso {
		if p.Offset != uint32(i*100) || p.Length != 100 || p.ActualLength != 50 || data[i*50] != byte(i+1) || data[i*50+49] != byte(i+1) {
			t.Fatalf("iso packet %d: %+v", i, p)
		}
	}

	// An unknown endpoint fails that one transfer, not the session.
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 5, Direction: proto.DirOut, Ep: 9, Length: 2}, []byte{1, 2}, nil)
	h, _, _ = v.recv(false)
	if h.Seqnum != 5 || h.Status != -usb.EINVAL {
		t.Fatalf("bad endpoint: %+v", h)
	}

	// Unlink a pending interrupt transfer: exactly one reply, RET_UNLINK.
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 6, Direction: proto.DirIn, Ep: 1, Length: 8}, nil, nil)
	v.send(proto.Header{Command: proto.CmdUnlink, Seqnum: 7, UnlinkSeqnum: 6}, nil, nil)
	h, _, _ = v.recv(true)
	if h.Command != proto.RetUnlink || h.Seqnum != 7 || h.Status != -usb.ECONNRESET {
		t.Fatalf("unlink pending: %+v", h)
	}
	// Unlinking something already finished reports success.
	v.send(proto.Header{Command: proto.CmdUnlink, Seqnum: 8, UnlinkSeqnum: 2}, nil, nil)
	h, _, _ = v.recv(true)
	if h.Command != proto.RetUnlink || h.Seqnum != 8 || h.Status != 0 {
		t.Fatalf("unlink finished: %+v", h)
	}

	// A second client cannot take the device while it is attached.
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := client.Import(c2, "1-1"); err == nil {
		t.Fatal("second import succeeded")
	}

	// Dropping the connection gives the device back.
	v.c.Close()
	select {
	case <-b.dev.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("device not closed after disconnect")
	}
}

func TestNames(t *testing.T) {
	_, addr, _ := start(t)
	names, err := client.Names(addr)
	if err != nil || len(names) != 1 || names["1-1"] != "Acme Wheel Base" {
		t.Fatalf("%v %v", err, names)
	}
}

// A device in use can be taken over by another client (which then has it
// reserved for a moment), and the server machine can keep devices for
// itself; holders reports all of it.
func TestReleaseAndHolders(t *testing.T) {
	b, addr, _ := start(t)
	v := attach(t, addr)
	h, err := client.Holders(addr)
	if err != nil || h["1-1"].Peer != "127.0.0.1" || h["1-1"].Kept {
		t.Fatalf("holders: %v %+v", err, h)
	}
	// From here the client counts as "another machine": it asks for the
	// device for itself, so its own session is left alone.
	if err := client.Release(addr, "1-1", proto.ReleaseForMe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.dev.closed:
		t.Fatal("the asking client's own session was dropped")
	default:
	}
	// Keep it on the server machine (allowed: this is loopback): the
	// session ends and the device returns to the host.
	if err := client.Release(addr, "1-1", proto.ReleaseKeep); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.dev.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("device not taken back")
	}
	v.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := proto.ReadHeader(v.c); err == nil {
		t.Fatal("the client's connection should have been closed")
	}
	b.mu.Lock()
	b.busy = false
	b.mu.Unlock()
	h, _ = client.Holders(addr)
	if !h["1-1"].Kept || h["1-1"].Peer != "" {
		t.Fatalf("kept device not reported: %+v", h)
	}
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	if _, err := client.Import(c, "1-1"); err == nil {
		t.Fatal("a kept device was handed out")
	}
	// Share again: clients may take it.
	if err := client.Release(addr, "", proto.ReleaseShare); err != nil {
		t.Fatal(err)
	}
	attach(t, addr)
}

// Pressing the escape keys on a shared keyboard gives every device back to
// the server machine and keeps it there.
func TestEscapeKeys(t *testing.T) {
	b, addr, s := start(t)
	c, err := ParseCombo("ctrl+alt+shift+f12")
	if err != nil || c.String() != "Ctrl+Alt+Shift+F12" {
		t.Fatalf("%v %v", c, err)
	}
	s.Escape = c
	b.keyboard = true
	v := attach(t, addr)
	for i := 0; i < 2; i++ {
		v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: uint32(10 + i), Direction: proto.DirIn, Ep: 1, Length: 8}, nil, nil)
	}
	time.Sleep(50 * time.Millisecond)
	// Left Ctrl + right Alt + left Shift, with F12 (0x45): nothing happens
	// while the keys are held...
	b.dev.press([]byte{0x01 | 0x40 | 0x02, 0, 0x45, 0, 0, 0, 0, 0})
	for i := 0; i < 2; i++ { // the reports reach the client
		v.recv(true)
	}
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 20, Direction: proto.DirIn, Ep: 1, Length: 8}, nil, nil)
	time.Sleep(100 * time.Millisecond)
	select {
	case <-b.dev.closed:
		t.Fatal("acted while the keys were still held")
	default:
	}
	// ...and once they are let go, the client sees the release first.
	b.dev.press(make([]byte, 8))
	if h, data, _ := v.recv(true); h.Seqnum != 20 || !reportIdle(data) {
		t.Fatalf("key release not passed on first: %+v %v", h, data)
	}
	select {
	case <-b.dev.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("escape keys did not give the device back")
	}
	b.mu.Lock()
	b.busy = false
	b.mu.Unlock()
	if h, _ := client.Holders(addr); !h["1-1"].Kept {
		t.Fatalf("device not kept after the escape keys: %+v", h)
	}
	// Taking it over from a client ends "kept".
	if err := client.Release(addr, "1-1", proto.ReleaseForMe); err != nil {
		t.Fatal(err)
	}
	attach(t, addr)
}

// When the server takes a keyboard away, the computer losing it is first
// told that no key is held.
func TestKeysReleasedOnDrop(t *testing.T) {
	b, addr, _ := start(t)
	b.keyboard = true
	v := attach(t, addr)
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 30, Direction: proto.DirIn, Ep: 1, Length: 8}, nil, nil)
	time.Sleep(50 * time.Millisecond)
	go client.Release(addr, "1-1", proto.ReleaseKeep)
	h, data, _ := v.recv(true)
	if h.Command != proto.RetSubmit || h.Seqnum != 30 || h.Length != 8 || !reportIdle(data) {
		t.Fatalf("no 'all keys up' before the keyboard was taken: %+v %v", h, data)
	}
}

// A gaming keyboard's second interface (numbered reports) left with Win
// held gets a release in its own format, report number kept.
func TestKeysReleasedOnDropNumbered(t *testing.T) {
	b, addr, _ := start(t)
	b.keyboard = true
	v := attach(t, addr)
	// Win held, reported on ep2 as report 1 (modifiers in the second byte).
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 40, Direction: proto.DirIn, Ep: 1, Length: 8}, nil, nil)
	time.Sleep(50 * time.Millisecond)
	b.dev.press([]byte{0x00, 0, 0, 0, 0, 0, 0, 0}) // ep1: nothing held
	v.recv(true)
	b.dev.mu.Lock()
	b.dev.ep2report = []byte{0x01, 0x08, 0, 0, 0, 0}
	b.dev.mu.Unlock()
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 41, Direction: proto.DirIn, Ep: 2, Length: 64}, nil, nil)
	if h, data, _ := v.recv(true); h.Seqnum != 41 || data[1] != 0x08 {
		t.Fatalf("setup: %+v %v", h, data)
	}
	v.send(proto.Header{Command: proto.CmdSubmit, Seqnum: 42, Direction: proto.DirIn, Ep: 2, Length: 64}, nil, nil)
	time.Sleep(50 * time.Millisecond)
	go client.Release(addr, "1-1", proto.ReleaseKeep)
	h, data, _ := v.recv(true)
	if h.Seqnum != 42 || len(data) != 6 || data[0] != 0x01 || data[1] != 0 {
		t.Fatalf("no release in the interface's own format: %+v %v", h, data)
	}
}

func TestComboMatching(t *testing.T) {
	c, _ := ParseCombo("Ctrl+Alt+Shift+F12")
	if !c.matchReport([]byte{0x07, 0, 0x45, 0, 0, 0, 0, 0}) || !c.matchReport([]byte{1, 0x70, 0, 0, 0x45, 0, 0, 0, 0}) {
		t.Fatal("held combination not seen")
	}
	if c.matchReport([]byte{0x03, 0, 0x45, 0, 0, 0, 0, 0}) || c.matchReport([]byte{0x07, 0, 0x44, 0, 0, 0, 0, 0}) {
		t.Fatal("seen without Alt, or with the wrong key")
	}
	if !c.matchKeys(map[uint16]bool{97: true, 100: true, 42: true, 88: true}) || c.matchKeys(map[uint16]bool{29: true, 56: true, 88: true}) {
		t.Fatal("local key matching")
	}
	for _, bad := range []string{"F12", "Hyper+F12", "Ctrl+Banana"} {
		if _, err := ParseCombo(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReservation(t *testing.T) {
	s := &Server{}
	s.release("1-1", proto.ReleaseForMe, &net.TCPAddr{IP: net.ParseIP("10.0.0.2")})
	if why := s.blocked("1-1", "10.0.0.3"); why == "" {
		t.Fatal("another client could take a device reserved for someone else")
	}
	if why := s.blocked("1-1", "10.0.0.2"); why != "" {
		t.Fatalf("the client it was reserved for was refused: %s", why)
	}
	if st := s.release("1-1", proto.ReleaseKeep, &net.TCPAddr{IP: net.ParseIP("10.0.0.2")}); st != proto.StNA {
		t.Fatal("a remote client was allowed to keep a device on the server")
	}
}

func TestKinds(t *testing.T) {
	_, addr, _ := start(t)
	k, err := client.Kinds(addr)
	if err != nil || k["1-1"] != "Game controller" {
		t.Fatalf("%v %v", err, k)
	}
}

func TestAllowList(t *testing.T) {
	_, n, _ := net.ParseCIDR("10.99.0.0/16")
	s := &Server{Allow: []*net.IPNet{n}}
	if s.Allowed(&net.TCPAddr{IP: net.ParseIP("192.168.1.20")}) {
		t.Fatal("client outside allow list was served")
	}
	if !s.Allowed(&net.TCPAddr{IP: net.ParseIP("10.99.3.4")}) {
		t.Fatal("client inside allow list was refused")
	}
	// The server machine itself is always let in: that is how it
	// releases and shares its own devices.
	if !s.Allowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.1")}) {
		t.Fatal("the server machine itself was refused")
	}
}
