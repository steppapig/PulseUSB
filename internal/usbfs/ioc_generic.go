//go:build linux && !(mips || mipsle || mips64 || mips64le || ppc64 || ppc64le)

package usbfs

// ioctl request encoding used by most Linux architectures.
const (
	iocNone     = 0
	iocWrite    = 1
	iocRead     = 2
	iocDirShift = 30
)
