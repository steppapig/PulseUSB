// Package server exports USB devices to USB/IP clients over TCP.
package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

const (
	maxTransfer   = 32 << 20 // largest transfer buffer accepted from a client
	maxIsoPackets = 1024
)

// Server accepts USB/IP connections and serves devices from a Backend.
type Server struct {
	Backend usb.Backend
	// Allow, when non-empty, restricts clients to these networks.
	Allow []*net.IPNet
	Logf  func(format string, args ...any)
	// Trace logs every USB request and its result.
	Trace bool
	// Escape, when set, is the key combination that gives every device
	// back to the server machine (see escape.go).
	Escape *Combo

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
	shut  bool

	// Who holds which device, which devices the server machine keeps for
	// itself, and short reservations made when a client takes a device over.
	dmu      sync.Mutex
	held     map[string]*holder
	kept     map[string]bool
	reserved map[string]reservation
}

type holder struct {
	sess *session // set once the device is handed over
	conn net.Conn
	ip   string
	done chan struct{} // closed once the device is back with the server
}

type reservation struct {
	ip    string
	until time.Time
}

// reserveFor is how long a device taken over stays reserved for the client
// that asked, so that nobody else can grab it before that client does.
const reserveFor = 5 * time.Second

func ipOf(a net.Addr) string {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.String()
	}
	host, _, _ := net.SplitHostPort(a.String())
	return host
}

func isLoopback(a net.Addr) bool {
	ip := net.ParseIP(ipOf(a))
	return ip != nil && ip.IsLoopback()
}

// blocked reports why a client at ip may not take a device, or "".
func (s *Server) blocked(busid, ip string) string {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if s.kept[busid] {
		return "kept on the server machine"
	}
	if r, ok := s.reserved[busid]; ok && time.Now().Before(r.until) && r.ip != ip {
		return "reserved for " + r.ip
	}
	return ""
}

// drop takes a device away from the client holding it, if any, and waits
// until it is back with the server. except leaves a client at that address
// alone (it is the one asking).
func (s *Server) drop(busid, except string) {
	s.dmu.Lock()
	h := s.held[busid]
	s.dmu.Unlock()
	if h == nil || (except != "" && h.ip == except) {
		return
	}
	s.dmu.Lock()
	sess := h.sess
	s.dmu.Unlock()
	if sess != nil {
		sess.releaseKeys() // so no key stays "held" on the computer losing it
	}
	h.conn.Close()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
	}
}

// release carries out a release request; "" means every device.
func (s *Server) release(busid string, mode uint32, from net.Addr) uint32 {
	if mode != proto.ReleaseForMe && !isLoopback(from) {
		return proto.StNA // only the server machine itself may keep or share
	}
	ip := ipOf(from)
	var targets []string
	s.dmu.Lock()
	if s.kept == nil {
		s.kept, s.reserved = map[string]bool{}, map[string]reservation{}
	}
	if busid == "" {
		for b := range s.held {
			targets = append(targets, b)
		}
		switch mode {
		case proto.ReleaseKeep:
			s.dmu.Unlock()
			s.keepAll()
			return proto.StOK
		case proto.ReleaseShare:
			s.kept = map[string]bool{}
		case proto.ReleaseForMe:
			s.kept = map[string]bool{} // taking over beats "kept"
		}
	} else {
		targets = []string{busid}
		switch mode {
		case proto.ReleaseKeep:
			s.kept[busid] = true
		case proto.ReleaseShare, proto.ReleaseForMe:
			delete(s.kept, busid) // taking over beats "kept" too
		}
	}
	if mode == proto.ReleaseForMe {
		for _, b := range targets {
			s.reserved[b] = reservation{ip: ip, until: time.Now().Add(reserveFor)}
		}
	}
	s.dmu.Unlock()
	if mode == proto.ReleaseShare {
		return proto.StOK
	}
	for _, b := range targets {
		except := ""
		if mode == proto.ReleaseForMe {
			except = ip // the client asking may already hold it
		}
		s.drop(b, except)
	}
	return proto.StOK
}

// holders lists the devices in use and those kept by the server machine.
func (s *Server) holders() []byte {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	type rec struct {
		busid, peer string
		kept        bool
	}
	var recs []rec
	seen := map[string]bool{}
	for b, h := range s.held {
		recs = append(recs, rec{b, h.ip, s.kept[b]})
		seen[b] = true
	}
	for b := range s.kept {
		if !seen[b] {
			recs = append(recs, rec{b, "", true})
		}
	}
	body := make([]byte, 4, 4+len(recs)*(proto.BusIDSize+proto.PeerSize+4))
	body[2], body[3] = byte(len(recs)>>8), byte(len(recs))
	for _, r := range recs {
		b := make([]byte, proto.BusIDSize+proto.PeerSize+4)
		copy(b[:proto.BusIDSize-1], r.busid)
		copy(b[proto.BusIDSize:proto.BusIDSize+proto.PeerSize-1], r.peer)
		if r.kept {
			b[proto.BusIDSize+proto.PeerSize] = 1
		}
		body = append(body, b...)
	}
	return body
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// readBusID reads a fixed-size, zero-padded bus id.
func readBusID(r io.Reader) (string, error) {
	var b [proto.BusIDSize]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), nil
		}
	}
	return string(b[:]), nil
}

// Allowed reports whether a client address may use the server. The server
// machine itself is always allowed (it releases and shares devices that way).
func (s *Server) Allowed(addr net.Addr) bool {
	if len(s.Allow) == 0 || isLoopback(addr) {
		return true
	}
	var ip net.IP
	switch t := addr.(type) {
	case *net.TCPAddr:
		ip = t.IP
	case *net.UDPAddr:
		ip = t.IP
	default:
		return false
	}
	for _, n := range s.Allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		if !s.Allowed(c.RemoteAddr()) {
			s.logf("%s: rejected (not in allow list)", c.RemoteAddr())
			c.Close()
			continue
		}
		if t, ok := c.(*net.TCPConn); ok {
			t.SetNoDelay(true)
			// A client that vanishes without saying goodbye (PC crash,
			// sleep, pulled cable) must be noticed quickly: until then its
			// devices stay reserved and cannot be attached again.
			t.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: 5 * time.Second, Interval: 2 * time.Second, Count: 3})
			setUserTimeout(t, 10*time.Second)
		}
		s.mu.Lock()
		if s.shut {
			s.mu.Unlock()
			c.Close()
			continue
		}
		if s.conns == nil {
			s.conns = map[net.Conn]struct{}{}
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.handle(c)
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

// Shutdown drops all clients and waits until every device has been handed
// back to the host.
func (s *Server) Shutdown() {
	s.mu.Lock()
	s.shut = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	peer := conn.RemoteAddr()
	br := bufio.NewReaderSize(conn, 64<<10)
	for {
		code, status, err := proto.ReadOp(br)
		if err != nil {
			if err != io.EOF {
				s.logf("%s: %v", peer, err)
			}
			return
		}
		switch code {
		case proto.OpReqDevlist:
			devs, err := s.Backend.List()
			if err != nil {
				s.logf("%s: list: %v", peer, err)
				proto.WriteOp(conn, proto.OpRepDevlist, proto.StError, nil)
				return
			}
			body := []byte{byte(len(devs) >> 24), byte(len(devs) >> 16), byte(len(devs) >> 8), byte(len(devs))}
			for _, d := range devs {
				body = d.Append(body, true)
			}
			if err := proto.WriteOp(conn, proto.OpRepDevlist, proto.StOK, body); err != nil {
				return
			}
		case proto.OpReqNames:
			names := map[string]string{}
			if n, ok := s.Backend.(usb.Namer); ok {
				names = n.Names()
			}
			body := []byte{0, 0, 0, 0}
			count := 0
			for busid, name := range names {
				if len(busid) >= proto.BusIDSize {
					continue
				}
				rec := make([]byte, proto.BusIDSize+proto.NameSize)
				copy(rec, busid)
				copy(rec[proto.BusIDSize:len(rec)-1], name)
				body = append(body, rec...)
				count++
			}
			body[2], body[3] = byte(count>>8), byte(count)
			if err := proto.WriteOp(conn, proto.OpRepNames, proto.StOK, body); err != nil {
				return
			}
		case proto.OpReqKinds:
			kinds := map[string]string{}
			if k, ok := s.Backend.(usb.Kinder); ok {
				kinds = k.Kinds()
			}
			body := []byte{0, 0, 0, 0}
			count := 0
			for busid, kind := range kinds {
				if len(busid) >= proto.BusIDSize {
					continue
				}
				rec := make([]byte, proto.BusIDSize+proto.KindSize)
				copy(rec, busid)
				copy(rec[proto.BusIDSize:len(rec)-1], kind)
				body = append(body, rec...)
				count++
			}
			body[2], body[3] = byte(count>>8), byte(count)
			if err := proto.WriteOp(conn, proto.OpRepKinds, proto.StOK, body); err != nil {
				return
			}
		case proto.OpReqRelease:
			busid, err := readBusID(br)
			if err != nil {
				return
			}
			st := s.release(busid, status, peer)
			if st == proto.StOK {
				what := busid
				if what == "" {
					what = "every device"
				}
				s.logf("%s: release %s (mode %d)", peer, what, status)
			}
			if err := proto.WriteOp(conn, proto.OpRepRelease, st, nil); err != nil {
				return
			}
		case proto.OpReqHolders:
			if err := proto.WriteOp(conn, proto.OpRepHolders, proto.StOK, s.holders()); err != nil {
				return
			}
		case proto.OpReqImport:
			busid, err := readBusID(br)
			if err != nil {
				return
			}
			if why := s.blocked(busid, ipOf(peer)); why != "" {
				s.logf("%s: import %s: %s", peer, busid, why)
				proto.WriteOp(conn, proto.OpRepImport, proto.StDevBusy, nil)
				return
			}
			dev, info, err := s.Backend.Open(busid)
			if err != nil {
				s.logf("%s: import %s: %v", peer, busid, err)
				st := uint32(proto.StDevErr)
				if errors.Is(err, usb.ErrBusy) {
					st = proto.StDevBusy
				} else if errors.Is(err, usb.ErrNotFound) {
					st = proto.StNoDev
				}
				proto.WriteOp(conn, proto.OpRepImport, st, nil)
				return
			}
			s.logf("%s: attached %s (%04x:%04x)", peer, busid, info.VendorID, info.ProductID)
			h := &holder{conn: conn, ip: ipOf(peer), done: make(chan struct{})}
			s.dmu.Lock()
			if s.held == nil {
				s.held = map[string]*holder{}
			}
			s.held[busid] = h
			s.dmu.Unlock()
			err = proto.WriteOp(conn, proto.OpRepImport, proto.StOK, info.Append(nil, false))
			if err == nil {
				ss := &session{conn: conn, dev: dev, pending: map[uint32]*pending{}}
				if h, ok := dev.(usb.HIDDevice); ok {
					ss.hid, ss.last = h.HIDEndpoints(), map[uint8]map[int][]byte{}
				}
				if k, ok := dev.(KeyboardEndpoints); ok {
					if eps := k.KeyboardEndpoints(); len(eps) > 0 {
						ss.keys = eps
						if s.Escape != nil {
							w := &escapeWatch{}
							ss.onKeys = func(r []byte) { s.checkReport(w, r) }
						}
					}
				}
				s.dmu.Lock()
				h.sess = ss
				s.dmu.Unlock()
				if s.Trace {
					ss.trace = func(format string, args ...any) { s.logf(busid+": "+format, args...) }
				}
				err = ss.run(br)
			}
			conn.Close() // unblock completions before waiting for the device
			dev.Close()
			s.dmu.Lock()
			if s.held[busid] == h {
				delete(s.held, busid)
			}
			s.dmu.Unlock()
			close(h.done)
			s.logf("%s: detached %s (%v)", peer, busid, err)
			return
		default:
			s.logf("%s: unknown operation %#04x", peer, code)
			return
		}
	}
}

// describe renders a transfer for the trace log.
func describe(t *usb.Transfer) string {
	dir := "out"
	if t.In {
		dir = "in"
	}
	if t.Endpoint == 0 {
		return fmt.Sprintf("control %s setup=% x", dir, t.Setup[:])
	}
	return fmt.Sprintf("ep%d %s", t.Endpoint, dir)
}

type pending struct {
	t         *usb.Transfer
	unlinked  bool
	unlinkSeq uint32
}

// session forwards URBs between one client and one device.
type session struct {
	conn net.Conn
	dev  usb.Device

	wmu sync.Mutex // serialises writes to conn

	trace func(format string, args ...any) // nil unless tracing

	// keys are the keyboard endpoints whose reports go to onKeys (for the
	// escape keys); nil for devices that are not keyboards.
	keys   map[uint8]bool
	onKeys func([]byte)
	// hid describes the HID input endpoints; last holds each one's latest
	// report, per report number (-1 without numbers), to know what is held.
	hid  map[uint8]usb.HIDEndpoint
	last map[uint8]map[int][]byte

	mu      sync.Mutex
	pending map[uint32]*pending
}

// remember keeps a HID endpoint's latest report, per report number.
func (s *session) remember(ep uint8, r []byte) {
	id := -1
	if s.hid[ep].ReportIDs {
		id = int(r[0])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last[ep] == nil {
		s.last[ep] = map[int][]byte{}
	}
	s.last[ep][id] = append([]byte(nil), r...)
}

// releaseKeys answers a keyboard's or mouse's waiting requests with
// "nothing pressed" before the server takes the device away, so the
// computer losing it does not keep keys held (Ctrl, Win and the like) that
// were down at the time. Each HID interface gets a release in its own
// format: for every report number that last showed something pressed, the
// same report with nothing in it. Standard keyboards that sent nothing yet
// get the plain 8-byte "no keys" report.
func (s *session) releaseKeys() {
	s.mu.Lock()
	byEP := map[uint8][]uint32{} // endpoint -> waiting requests
	for seq, p := range s.pending {
		ep := p.t.Endpoint
		if !p.unlinked && p.t.In && (s.hid[ep].Releasable || s.keys[ep]) {
			byEP[ep] = append(byEP[ep], seq)
		}
	}
	type answer struct {
		t    *usb.Transfer
		data []byte
	}
	var answers []answer
	for ep, seqs := range byEP {
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		var releases [][]byte
		for id, r := range s.last[ep] {
			start := 0
			if id >= 0 {
				start = 1
			}
			held := false
			for _, b := range r[start:] {
				held = held || b != 0
			}
			if held {
				rel := make([]byte, len(r))
				if id >= 0 {
					rel[0] = byte(id)
				}
				releases = append(releases, rel)
			}
		}
		if len(releases) == 0 && s.keys[ep] {
			releases = [][]byte{make([]byte, 8)}
		}
		for i, seq := range seqs {
			if i >= len(releases) {
				break
			}
			t := s.pending[seq].t
			delete(s.pending, seq)
			answers = append(answers, answer{t, releases[i]})
		}
	}
	s.mu.Unlock()
	for _, a := range answers {
		buf := make([]byte, len(a.t.Buffer))
		n := copy(buf, a.data)
		s.writeRetSubmit(&usb.Transfer{Seqnum: a.t.Seqnum, Endpoint: a.t.Endpoint, In: true, Buffer: buf, ActualLength: int32(n)})
		s.dev.Cancel(a.t)
	}
}

func (s *session) run(br *bufio.Reader) error {
	for {
		h, err := proto.ReadHeader(br)
		if err != nil {
			return err
		}
		switch h.Command {
		case proto.CmdSubmit:
			if err := s.submit(br, h); err != nil {
				return err
			}
		case proto.CmdUnlink:
			s.mu.Lock()
			p := s.pending[h.UnlinkSeqnum]
			if p != nil && !p.unlinked {
				p.unlinked, p.unlinkSeq = true, h.Seqnum
			} else {
				p = nil
			}
			s.mu.Unlock()
			if s.trace != nil {
				s.trace("cancel #%d (pending: %v)", h.UnlinkSeqnum, p != nil)
			}
			if p != nil {
				// The reply is sent when the cancelled transfer completes.
				s.dev.Cancel(p.t)
			} else if err := s.writeRetUnlink(h.Seqnum, 0); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected command %d from client", h.Command)
		}
	}
}

func (s *session) submit(br *bufio.Reader, h *proto.Header) error {
	if h.Length < 0 || h.Length > maxTransfer {
		return fmt.Errorf("invalid transfer length %d", h.Length)
	}
	if h.Ep > 15 {
		return fmt.Errorf("invalid endpoint %d", h.Ep)
	}
	t := &usb.Transfer{
		Seqnum:     h.Seqnum,
		Endpoint:   uint8(h.Ep),
		In:         h.Direction == proto.DirIn,
		Flags:      h.Flags,
		Setup:      h.Setup,
		Buffer:     make([]byte, h.Length),
		StartFrame: h.StartFrame,
		Interval:   h.Interval,
	}
	if !t.In {
		if _, err := io.ReadFull(br, t.Buffer); err != nil {
			return err
		}
	}
	// Non-isochronous transfers carry 0 (or -1 from some clients) here.
	if n := h.NumPackets; n > 0 {
		if n > maxIsoPackets {
			return fmt.Errorf("too many isochronous packets (%d)", n)
		}
		raw := make([]byte, int(n)*proto.IsoDescSize)
		if _, err := io.ReadFull(br, raw); err != nil {
			return err
		}
		t.Iso = make([]proto.IsoDesc, n)
		for i := range t.Iso {
			d := &t.Iso[i]
			d.Unmarshal(raw[i*proto.IsoDescSize:])
			if uint64(d.Offset)+uint64(d.Length) > uint64(len(t.Buffer)) {
				return fmt.Errorf("isochronous packet outside transfer buffer")
			}
			d.ActualLength, d.Status = 0, 0
		}
	}

	s.mu.Lock()
	if _, dup := s.pending[t.Seqnum]; dup {
		s.mu.Unlock()
		return fmt.Errorf("duplicate seqnum %d", t.Seqnum)
	}
	s.pending[t.Seqnum] = &pending{t: t}
	s.mu.Unlock()

	if s.trace != nil {
		s.trace("#%d %s len=%d flags=%#x packets=%d", t.Seqnum, describe(t), len(t.Buffer), t.Flags, len(t.Iso))
	}
	if err := s.dev.Submit(t, s.done); err != nil {
		if s.trace != nil {
			s.trace("#%d rejected by host: %v", t.Seqnum, err)
		}
		t.Status = usb.StatusFromError(err)
		t.ActualLength = 0
		s.done(t)
	}
	return nil
}

// done is called by the device backend when a transfer completes.
func (s *session) done(t *usb.Transfer) {
	s.mu.Lock()
	p := s.pending[t.Seqnum]
	delete(s.pending, t.Seqnum)
	s.mu.Unlock()
	if p == nil {
		return
	}
	if s.trace != nil {
		s.trace("#%d done status=%d actual=%d cancelled=%v", t.Seqnum, t.Status, t.ActualLength, p.unlinked)
	}
	if p.unlinked {
		s.writeRetUnlink(p.unlinkSeq, -usb.ECONNRESET)
	} else {
		s.writeRetSubmit(t)
		if t.In && t.Status == 0 && t.ActualLength > 0 && s.hid[t.Endpoint].Releasable {
			s.remember(t.Endpoint, t.Buffer[:t.ActualLength])
		}
		if s.onKeys != nil && t.In && t.Status == 0 && s.keys[t.Endpoint] && t.ActualLength > 0 {
			s.onKeys(t.Buffer[:t.ActualLength])
		}
	}
	if t.Status == -usb.ENODEV || t.Status == -usb.ESHUTDOWN {
		// The device was unplugged: drop the client so it sees a disconnect.
		s.conn.Close()
	}
}

func (s *session) writeRetUnlink(seq uint32, status int32) error {
	h := proto.Header{Command: proto.RetUnlink, Seqnum: seq, Status: status}
	var b [proto.HeaderSize]byte
	h.Marshal(b[:])
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.conn.Write(b[:])
	return err
}

func (s *session) writeRetSubmit(t *usb.Transfer) error {
	h := proto.Header{
		Command:    proto.RetSubmit,
		Seqnum:     t.Seqnum,
		Status:     t.Status,
		StartFrame: t.StartFrame,
		NumPackets: int32(len(t.Iso)),
		ErrorCount: t.ErrorCount,
	}
	actual := int(t.ActualLength)
	if actual < 0 {
		actual = 0
	}
	if actual > len(t.Buffer) {
		actual = len(t.Buffer)
	}
	if len(t.Iso) > 0 {
		actual = 0
		for i := range t.Iso {
			if d := &t.Iso[i]; d.ActualLength > d.Length {
				d.ActualLength = d.Length
			}
			actual += int(t.Iso[i].ActualLength)
		}
	}
	h.Length = int32(actual)

	out := make([]byte, proto.HeaderSize, proto.HeaderSize+actual+len(t.Iso)*proto.IsoDescSize)
	h.Marshal(out)
	if t.In {
		if len(t.Iso) > 0 {
			// Isochronous IN data travels packed: only the bytes actually
			// received for each packet, back to back.
			for i := range t.Iso {
				d := &t.Iso[i]
				out = append(out, t.Buffer[d.Offset:d.Offset+d.ActualLength]...)
			}
		} else {
			out = append(out, t.Buffer[:actual]...)
		}
	}
	for i := range t.Iso {
		out = t.Iso[i].Append(out)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.conn.Write(out)
	return err
}
