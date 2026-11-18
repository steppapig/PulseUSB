//go:build linux && (mips || mipsle || mips64 || mips64le || ppc64 || ppc64le)

package usbfs

// ioctl request encoding used by MIPS and PowerPC.
const (
	iocNone     = 1
	iocRead     = 2
	iocWrite    = 4
	iocDirShift = 29
)
