// Package usb holds the types shared between the network server and the
// device backends.
package usb

import (
	"errors"
	"syscall"

	"pulseusb/internal/proto"
)

// Transfer flags as used on the wire (Linux URB flags).
const (
	FlagShortNotOK = 0x0001
	FlagZeroPacket = 0x0040
)

// Status codes carried on the wire. USB/IP uses the generic Linux errno
// numbers; backends translate native values (which differ on e.g. MIPS).
const (
	ENOENT     = 2
	ENODEV     = 19
	EINVAL     = 22
	EPIPE      = 32
	ECONNRESET = 104
	ETIMEDOUT  = 110
	ESHUTDOWN  = 108
)

var (
	ErrBusy     = errors.New("device is already attached to a client")
	ErrNotFound = errors.New("no such exported device")
)

// Transfer is one USB request (URB) travelling to the device and back.
type Transfer struct {
	Seqnum   uint32
	Endpoint uint8 // endpoint number without the direction bit
	In       bool
	Flags    uint32
	Setup    [8]byte // control transfers only
	// Buffer holds the OUT payload, or is the space for IN data.
	Buffer     []byte
	Iso        []proto.IsoDesc
	StartFrame int32
	Interval   int32

	// Filled in on completion.
	Status       int32 // 0 or a negative generic errno
	ActualLength int32
	ErrorCount   int32
}

// Device is an opened USB device owned by one client session.
type Device interface {
	// Submit starts t. If it returns nil, done is called exactly once
	// (possibly before Submit returns); if it returns an error done is
	// never called.
	Submit(t *Transfer, done func(*Transfer)) error
	// Cancel asks for a pending transfer to be aborted; it still completes
	// through its done callback.
	Cancel(t *Transfer)
	// Close gives the device back to the host.
	Close() error
}

// Backend enumerates and opens exportable devices.
type Backend interface {
	List() ([]*proto.Device, error)
	Open(busid string) (Device, *proto.Device, error)
}

// HIDEndpoint describes an interrupt IN endpoint of a HID interface.
type HIDEndpoint struct {
	// Releasable: an all-zero report means "nothing pressed" (keyboards,
	// mice, media keys), so one can be sent when the device is taken away.
	Releasable bool
	// ReportIDs: reports start with a report number.
	ReportIDs bool
}

// HIDDevice is implemented by devices that can describe their HID inputs.
type HIDDevice interface {
	HIDEndpoints() map[uint8]HIDEndpoint
}

// Kinder is implemented by backends that can tell what kind of device each
// is ("Game controller", "Keyboard", ...), keyed by bus id.
type Kinder interface {
	Kinds() map[string]string
}

// Namer is implemented by backends that can tell what devices call
// themselves (manufacturer and product strings), keyed by bus id.
type Namer interface {
	Names() map[string]string
}

var generic = []struct {
	e syscall.Errno
	n int32
}{
	{syscall.EPERM, 1}, {syscall.ENOENT, 2}, {syscall.EIO, 5}, {syscall.ENXIO, 6},
	{syscall.EAGAIN, 11}, {syscall.ENOMEM, 12}, {syscall.EACCES, 13}, {syscall.EFAULT, 14},
	{syscall.EBUSY, 16}, {syscall.EXDEV, 18}, {syscall.ENODEV, 19}, {syscall.EINVAL, 22},
	{syscall.EFBIG, 27}, {syscall.ENOSPC, 28}, {syscall.EPIPE, 32}, {syscall.ETIME, 62},
	{syscall.ENOSR, 63}, {syscall.ECOMM, 70}, {syscall.EPROTO, 71}, {syscall.EOVERFLOW, 75},
	{syscall.EILSEQ, 84}, {syscall.EMSGSIZE, 90}, {syscall.EOPNOTSUPP, 95},
	{syscall.ECONNRESET, 104}, {syscall.ENOTCONN, 107}, {syscall.ESHUTDOWN, 108},
	{syscall.ETIMEDOUT, 110}, {syscall.EHOSTUNREACH, 113}, {syscall.EINPROGRESS, 115},
	{syscall.EREMOTEIO, 121},
}

// StatusFromErrno converts a native errno into a wire status.
func StatusFromErrno(e syscall.Errno) int32 {
	if e == 0 {
		return 0
	}
	for _, g := range generic {
		if g.e == e {
			return -g.n
		}
	}
	return -int32(e)
}

// StatusError is an error that already carries a wire status.
type StatusError int32

func (e StatusError) Error() string { return "usb transfer failed" }

// StatusFromError converts a Submit error into a wire status.
func StatusFromError(err error) int32 {
	var se StatusError
	if errors.As(err, &se) {
		return int32(se)
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		return StatusFromErrno(e)
	}
	return -EPIPE
}
