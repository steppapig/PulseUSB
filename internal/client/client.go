// Package client implements the client side of the USB/IP handshake.
package client

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"pulseusb/internal/proto"
	"pulseusb/internal/vhci"
)

// Addr adds the default port to a host name if it has none.
func Addr(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, proto.DefaultPort)
}

func dial(host string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", Addr(host), 3*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(8 * time.Second))
	return c, nil
}

// List asks a server for its exported devices.
func List(host string) ([]*proto.Device, error) {
	c, err := dial(host)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := proto.WriteOp(c, proto.OpReqDevlist, 0, nil); err != nil {
		return nil, err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return nil, err
	}
	if code != proto.OpRepDevlist || status != proto.StOK {
		return nil, fmt.Errorf("server refused device list (status %d)", status)
	}
	var nb [4]byte
	if _, err := io.ReadFull(c, nb[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(nb[:])
	if n > 4096 {
		return nil, fmt.Errorf("implausible device count %d", n)
	}
	devs := make([]*proto.Device, 0, n)
	for i := uint32(0); i < n; i++ {
		d, err := proto.ReadDevice(c, true)
		if err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// Kinds asks a server what each of its devices is ("Game controller",
// "Keyboard", ...). Servers that cannot tell yield an error.
func Kinds(host string) (map[string]string, error) {
	c, err := dial(host)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := proto.WriteOp(c, proto.OpReqKinds, 0, nil); err != nil {
		return nil, err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return nil, err
	}
	if code != proto.OpRepKinds || status != proto.StOK {
		return nil, fmt.Errorf("server does not report device kinds")
	}
	var nb [4]byte
	if _, err := io.ReadFull(c, nb[:]); err != nil {
		return nil, err
	}
	n := int(nb[2])<<8 | int(nb[3])
	out := map[string]string{}
	rec := make([]byte, proto.BusIDSize+proto.KindSize)
	cstr := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return strings.ToValidUTF8(string(b), "")
	}
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(c, rec); err != nil {
			return nil, err
		}
		out[cstr(rec[:proto.BusIDSize])] = cstr(rec[proto.BusIDSize:])
	}
	return out, nil
}

// Names asks a pulseusb server what its devices call themselves, keyed by bus
// id. Other USB/IP servers do not support this and yield an error.
func Names(host string) (map[string]string, error) {
	c, err := dial(host)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := proto.WriteOp(c, proto.OpReqNames, 0, nil); err != nil {
		return nil, err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return nil, err
	}
	if code != proto.OpRepNames || status != proto.StOK {
		return nil, fmt.Errorf("server does not report device names")
	}
	var nb [4]byte
	if _, err := io.ReadFull(c, nb[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(nb[:])
	if n > 4096 {
		return nil, fmt.Errorf("implausible device count %d", n)
	}
	out := map[string]string{}
	rec := make([]byte, proto.BusIDSize+proto.NameSize)
	cstr := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return strings.ToValidUTF8(string(b), "")
	}
	for i := uint32(0); i < n; i++ {
		if _, err := io.ReadFull(c, rec); err != nil {
			return nil, err
		}
		out[cstr(rec[:proto.BusIDSize])] = cstr(rec[proto.BusIDSize:])
	}
	return out, nil
}

// Holder says who uses a device: Peer is the address of the client holding
// it, Kept means the server machine keeps it for itself.
type Holder struct {
	Peer string
	Kept bool
}

// Holders asks a server which devices are in use. Servers that do not know
// the question (older versions, other USB/IP servers) yield an error.
func Holders(host string) (map[string]Holder, error) {
	h, _, err := HoldersAndSelf(host)
	return h, err
}

// HoldersAndSelf is Holders, plus the address the server sees this
// computer as, so the caller can tell its own devices from other computers'.
func HoldersAndSelf(host string) (map[string]Holder, string, error) {
	c, err := dial(host)
	if err != nil {
		return nil, "", err
	}
	defer c.Close()
	self := ""
	if a, ok := c.LocalAddr().(*net.TCPAddr); ok {
		self = a.IP.String()
	}
	h, err := readHolders(c)
	return h, self, err
}

func readHolders(c net.Conn) (map[string]Holder, error) {
	if err := proto.WriteOp(c, proto.OpReqHolders, 0, nil); err != nil {
		return nil, err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return nil, err
	}
	if code != proto.OpRepHolders || status != proto.StOK {
		return nil, fmt.Errorf("server does not report who uses its devices")
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return nil, err
	}
	n := int(head[2])<<8 | int(head[3])
	out := map[string]Holder{}
	if head[0]&1 != 0 {
		out["*"] = Holder{Kept: true} // every device is kept by the server machine
	}
	rec := make([]byte, proto.BusIDSize+proto.PeerSize+4)
	cstr := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return strings.ToValidUTF8(string(b), "")
	}
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(c, rec); err != nil {
			return nil, err
		}
		out[cstr(rec[:proto.BusIDSize])] = Holder{
			Peer: cstr(rec[proto.BusIDSize : proto.BusIDSize+proto.PeerSize]),
			Kept: rec[proto.BusIDSize+proto.PeerSize]&1 != 0,
		}
	}
	return out, nil
}

// Release asks a server to free a device ("" for every device). With
// proto.ReleaseForMe it is taken from whoever holds it and held back
// briefly for this client.
func Release(host, busid string, mode uint32) error {
	if len(busid) >= proto.BusIDSize {
		return fmt.Errorf("bus id too long")
	}
	c, err := dial(host)
	if err != nil {
		return err
	}
	defer c.Close()
	body := make([]byte, proto.BusIDSize)
	copy(body, busid)
	if err := proto.WriteOp(c, proto.OpReqRelease, mode, body); err != nil {
		return err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return fmt.Errorf("the server cannot release devices (update it): %w", err)
	}
	if code != proto.OpRepRelease {
		return fmt.Errorf("unexpected reply %#04x", code)
	}
	if status == proto.StNA {
		return fmt.Errorf("only the server machine itself may do that")
	}
	if status != proto.StOK {
		return fmt.Errorf("the server refused (status %d)", status)
	}
	return nil
}

// Import requests a device; on success the connection is in the URB phase.
func Import(c net.Conn, busid string) (*proto.Device, error) {
	if len(busid) >= proto.BusIDSize {
		return nil, fmt.Errorf("bus id too long")
	}
	body := make([]byte, proto.BusIDSize)
	copy(body, busid)
	if err := proto.WriteOp(c, proto.OpReqImport, 0, body); err != nil {
		return nil, err
	}
	code, status, err := proto.ReadOp(c)
	if err != nil {
		return nil, err
	}
	if code != proto.OpRepImport {
		return nil, fmt.Errorf("unexpected reply %#04x", code)
	}
	switch status {
	case proto.StOK:
	case proto.StDevBusy:
		return nil, fmt.Errorf("device %s is in use by another client", busid)
	case proto.StNoDev:
		return nil, fmt.Errorf("server does not export a device %s", busid)
	default:
		return nil, fmt.Errorf("server could not hand over %s (status %d)", busid, status)
	}
	return proto.ReadDevice(c, false)
}

// Attach imports a device and plugs it into the local virtual controller.
// The kernel takes over the connection, so nothing needs to keep running.
func Attach(host, busid string, v *vhci.Controller) (int, error) {
	c, err := dial(host)
	if err != nil {
		return -1, err
	}
	defer c.Close()
	dev, err := Import(c, busid)
	if err != nil {
		return -1, err
	}
	c.SetDeadline(time.Time{})
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return -1, fmt.Errorf("not a TCP connection")
	}
	tc.SetNoDelay(true)
	tc.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: 5 * time.Second, Interval: 2 * time.Second, Count: 3})
	f, err := tc.File() // a blocking duplicate the kernel can adopt
	if err != nil {
		return -1, err
	}
	defer f.Close()
	port, err := v.Attach(int(f.Fd()), dev.DevID(), dev.Speed)
	if err != nil {
		return -1, err
	}
	h, p, _ := net.SplitHostPort(Addr(host))
	v.Record(port, h, p, busid)
	return port, nil
}
