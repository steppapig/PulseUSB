package main

import (
	"encoding/binary"
	"os"
	"sync"
	"unsafe"

	"pulseusb/internal/proto"
	"pulseusb/internal/usb"
)

// While a client has a device attached, the server asks the kernel to keep
// the processor from dropping into deep sleep states between USB reports.
// An idle machine (a laptop in particular) otherwise takes up to a
// millisecond or more to wake for each report, which is felt as occasional
// stutter in input devices. The request is made the standard way, by
// holding /dev/cpu_dma_latency open with the tolerated wake-up delay
// written to it, and is withdrawn when the last device is released.
const (
	qosDevice     = "/dev/cpu_dma_latency"
	wakeLatencyUs = 20 // light sleep states only
)

type awake struct {
	usb.Backend
	path string
	logf func(string, ...any)

	mu     sync.Mutex
	active int
	hold   *os.File
}

// lowLatency wraps a backend so that the machine stays responsive while
// devices are attached.
func lowLatency(b usb.Backend, logf func(string, ...any)) usb.Backend {
	return &awake{Backend: b, path: qosDevice, logf: logf}
}

// Kinds passes through the optional ability to tell what devices are.
func (a *awake) Kinds() map[string]string {
	if k, ok := a.Backend.(usb.Kinder); ok {
		return k.Kinds()
	}
	return nil
}

// Names passes through the optional naming ability of the wrapped backend.
func (a *awake) Names() map[string]string {
	if n, ok := a.Backend.(usb.Namer); ok {
		return n.Names()
	}
	return nil
}

func (a *awake) Open(busid string) (usb.Device, *proto.Device, error) {
	dev, info, err := a.Backend.Open(busid)
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	if a.active++; a.active == 1 {
		if f, err := os.OpenFile(a.path, os.O_WRONLY, 0); err != nil {
			a.logf("cannot keep the processor awake for low latency: %v", err)
		} else {
			var v [4]byte
			if *(*byte)(unsafe.Pointer(&[]uint16{1}[0])) == 1 {
				binary.LittleEndian.PutUint32(v[:], wakeLatencyUs)
			} else {
				binary.BigEndian.PutUint32(v[:], wakeLatencyUs)
			}
			if _, err := f.Write(v[:]); err != nil {
				a.logf("cannot keep the processor awake for low latency: %v", err)
				f.Close()
			} else {
				a.hold = f
				a.logf("keeping the processor in light sleep states while devices are attached")
			}
		}
	}
	a.mu.Unlock()
	return &awakeDevice{Device: dev, a: a}, info, nil
}

type awakeDevice struct {
	usb.Device
	a    *awake
	once sync.Once
}

func (d *awakeDevice) Close() error {
	err := d.Device.Close()
	d.once.Do(func() {
		a := d.a
		a.mu.Lock()
		if a.active--; a.active == 0 && a.hold != nil {
			a.hold.Close()
			a.hold = nil
		}
		a.mu.Unlock()
	})
	return err
}
