// Package proto implements the USB/IP wire protocol (version 1.1.1), the
// same protocol spoken by the Linux kernel's usbip-host / vhci-hcd drivers.
// All integers are big-endian on the wire.
package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	Version     = 0x0111
	DefaultPort = "3240"

	OpReqDevlist = 0x8005
	OpRepDevlist = 0x0005
	OpReqImport  = 0x8003
	OpRepImport  = 0x0003

	// pulseusb extension: the names devices report about themselves. Other
	// USB/IP servers do not know it and close the connection.
	OpReqNames = 0x80f0
	OpRepNames = 0x00f0
	NameSize   = 128

	// usbeth extension: free a device that another client holds. The
	// request carries a bus id ("" for every device) and, in the status
	// field, one of the Release modes.
	OpReqRelease = 0x80f1
	OpRepRelease = 0x00f1
	// usbeth extension: which devices are in use, and by whom.
	OpReqHolders = 0x80f2
	OpRepHolders = 0x00f2
	PeerSize     = 64
	// usbeth extension: what each device is (game controller, keyboard...),
	// from the description it gives of itself.
	OpReqKinds = 0x80f3
	OpRepKinds = 0x00f3
	KindSize   = 32

	// ReleaseForMe hands the device to the client asking: whoever holds it
	// loses it, and for a few seconds only the asking client may take it.
	ReleaseForMe = 0
	// ReleaseKeep gives the device back to the server machine itself and
	// keeps it there until ReleaseShare. Accepted from that machine only.
	ReleaseKeep = 1
	// ReleaseShare makes a kept device available to clients again.
	ReleaseShare = 2

	// Operation status codes.
	StOK      = 0
	StNA      = 1
	StDevBusy = 2
	StDevErr  = 3
	StNoDev   = 4
	StError   = 5

	CmdSubmit = 1
	CmdUnlink = 2
	RetSubmit = 3
	RetUnlink = 4

	DirOut = 0
	DirIn  = 1

	HeaderSize  = 48
	DeviceSize  = 312
	IsoDescSize = 16
	BusIDSize   = 32

	// Device speeds (enum usb_device_speed).
	SpeedLow       = 1
	SpeedFull      = 2
	SpeedHigh      = 3
	SpeedSuper     = 5
	SpeedSuperPlus = 6
)

var be = binary.BigEndian

func putString(b []byte, s string) { copy(b[:len(b)-1], s) }

func getString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// WriteOp writes an operation header followed by an optional body.
func WriteOp(w io.Writer, code uint16, status uint32, body []byte) error {
	b := make([]byte, 8, 8+len(body))
	be.PutUint16(b[0:], Version)
	be.PutUint16(b[2:], code)
	be.PutUint32(b[4:], status)
	_, err := w.Write(append(b, body...))
	return err
}

// ReadOp reads an operation header.
func ReadOp(r io.Reader) (code uint16, status uint32, err error) {
	var b [8]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return
	}
	if v := be.Uint16(b[0:]); v != Version {
		return 0, 0, fmt.Errorf("unsupported USB/IP version %#04x", v)
	}
	return be.Uint16(b[2:]), be.Uint32(b[4:]), nil
}

// Interface is one interface of an exported device.
type Interface struct{ Class, SubClass, Protocol uint8 }

// Device describes an exportable USB device.
type Device struct {
	Path                      string
	BusID                     string
	BusNum, DevNum, Speed     uint32
	VendorID, ProductID       uint16
	BcdDevice                 uint16
	Class, SubClass, Protocol uint8
	ConfigValue, NumConfigs   uint8
	Interfaces                []Interface
}

// DevID is the identifier vhci uses for the device.
func (d *Device) DevID() uint32 { return d.BusNum<<16 | d.DevNum }

// Append serialises the device; the interface list is only part of
// device-list replies.
func (d *Device) Append(out []byte, withInterfaces bool) []byte {
	b := make([]byte, DeviceSize)
	putString(b[0:256], d.Path)
	putString(b[256:288], d.BusID)
	be.PutUint32(b[288:], d.BusNum)
	be.PutUint32(b[292:], d.DevNum)
	be.PutUint32(b[296:], d.Speed)
	be.PutUint16(b[300:], d.VendorID)
	be.PutUint16(b[302:], d.ProductID)
	be.PutUint16(b[304:], d.BcdDevice)
	b[306], b[307], b[308] = d.Class, d.SubClass, d.Protocol
	b[309], b[310], b[311] = d.ConfigValue, d.NumConfigs, uint8(len(d.Interfaces))
	out = append(out, b...)
	if withInterfaces {
		for _, i := range d.Interfaces {
			out = append(out, i.Class, i.SubClass, i.Protocol, 0)
		}
	}
	return out
}

// ReadDevice parses a device record.
func ReadDevice(r io.Reader, withInterfaces bool) (*Device, error) {
	b := make([]byte, DeviceSize)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	d := &Device{
		Path:        getString(b[0:256]),
		BusID:       getString(b[256:288]),
		BusNum:      be.Uint32(b[288:]),
		DevNum:      be.Uint32(b[292:]),
		Speed:       be.Uint32(b[296:]),
		VendorID:    be.Uint16(b[300:]),
		ProductID:   be.Uint16(b[302:]),
		BcdDevice:   be.Uint16(b[304:]),
		Class:       b[306],
		SubClass:    b[307],
		Protocol:    b[308],
		ConfigValue: b[309],
		NumConfigs:  b[310],
	}
	n := int(b[311])
	if withInterfaces {
		ib := make([]byte, 4*n)
		if _, err := io.ReadFull(r, ib); err != nil {
			return nil, err
		}
		for i := 0; i < n; i++ {
			d.Interfaces = append(d.Interfaces, Interface{ib[4*i], ib[4*i+1], ib[4*i+2]})
		}
	} else {
		d.Interfaces = make([]Interface, n)
	}
	return d, nil
}

// Header is the 48-byte header of every packet in the URB phase. Which of
// the fields after Ep are meaningful depends on Command.
type Header struct {
	Command, Seqnum, Devid, Direction, Ep uint32

	Flags      uint32  // CMD_SUBMIT
	Length     int32   // CMD_SUBMIT: buffer length; RET_SUBMIT: actual length
	StartFrame int32   // CMD_SUBMIT, RET_SUBMIT
	NumPackets int32   // CMD_SUBMIT, RET_SUBMIT
	Interval   int32   // CMD_SUBMIT
	ErrorCount int32   // RET_SUBMIT
	Setup      [8]byte // CMD_SUBMIT

	Status       int32  // RET_SUBMIT, RET_UNLINK
	UnlinkSeqnum uint32 // CMD_UNLINK
}

// Marshal encodes h into b, which must hold at least HeaderSize bytes.
func (h *Header) Marshal(b []byte) {
	b = b[:HeaderSize]
	for i := range b {
		b[i] = 0
	}
	be.PutUint32(b[0:], h.Command)
	be.PutUint32(b[4:], h.Seqnum)
	be.PutUint32(b[8:], h.Devid)
	be.PutUint32(b[12:], h.Direction)
	be.PutUint32(b[16:], h.Ep)
	switch h.Command {
	case CmdSubmit:
		be.PutUint32(b[20:], h.Flags)
		be.PutUint32(b[24:], uint32(h.Length))
		be.PutUint32(b[28:], uint32(h.StartFrame))
		be.PutUint32(b[32:], uint32(h.NumPackets))
		be.PutUint32(b[36:], uint32(h.Interval))
		copy(b[40:], h.Setup[:])
	case RetSubmit:
		be.PutUint32(b[20:], uint32(h.Status))
		be.PutUint32(b[24:], uint32(h.Length))
		be.PutUint32(b[28:], uint32(h.StartFrame))
		be.PutUint32(b[32:], uint32(h.NumPackets))
		be.PutUint32(b[36:], uint32(h.ErrorCount))
	case CmdUnlink:
		be.PutUint32(b[20:], h.UnlinkSeqnum)
	case RetUnlink:
		be.PutUint32(b[20:], uint32(h.Status))
	}
}

// Unmarshal decodes a header from b.
func (h *Header) Unmarshal(b []byte) error {
	b = b[:HeaderSize]
	*h = Header{
		Command:   be.Uint32(b[0:]),
		Seqnum:    be.Uint32(b[4:]),
		Devid:     be.Uint32(b[8:]),
		Direction: be.Uint32(b[12:]),
		Ep:        be.Uint32(b[16:]),
	}
	switch h.Command {
	case CmdSubmit:
		h.Flags = be.Uint32(b[20:])
		h.Length = int32(be.Uint32(b[24:]))
		h.StartFrame = int32(be.Uint32(b[28:]))
		h.NumPackets = int32(be.Uint32(b[32:]))
		h.Interval = int32(be.Uint32(b[36:]))
		copy(h.Setup[:], b[40:])
	case RetSubmit:
		h.Status = int32(be.Uint32(b[20:]))
		h.Length = int32(be.Uint32(b[24:]))
		h.StartFrame = int32(be.Uint32(b[28:]))
		h.NumPackets = int32(be.Uint32(b[32:]))
		h.ErrorCount = int32(be.Uint32(b[36:]))
	case CmdUnlink:
		h.UnlinkSeqnum = be.Uint32(b[20:])
	case RetUnlink:
		h.Status = int32(be.Uint32(b[20:]))
	default:
		return fmt.Errorf("unknown USB/IP command %d", h.Command)
	}
	return nil
}

// ReadHeader reads and decodes one URB-phase header.
func ReadHeader(r io.Reader) (*Header, error) {
	var b [HeaderSize]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return nil, err
	}
	h := new(Header)
	return h, h.Unmarshal(b[:])
}

// IsoDesc is the per-packet descriptor of an isochronous transfer.
type IsoDesc struct {
	Offset, Length, ActualLength uint32
	Status                       int32
}

func (d *IsoDesc) Append(out []byte) []byte {
	var b [IsoDescSize]byte
	be.PutUint32(b[0:], d.Offset)
	be.PutUint32(b[4:], d.Length)
	be.PutUint32(b[8:], d.ActualLength)
	be.PutUint32(b[12:], uint32(d.Status))
	return append(out, b[:]...)
}

func (d *IsoDesc) Unmarshal(b []byte) {
	d.Offset = be.Uint32(b[0:])
	d.Length = be.Uint32(b[4:])
	d.ActualLength = be.Uint32(b[8:])
	d.Status = int32(be.Uint32(b[12:]))
}
