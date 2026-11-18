package server

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"
)

// WatchLocalKeyboards watches the server machine's own keyboards for the
// escape keys, which share the devices again. Keyboards come and go (a
// shared keyboard disappears from this machine while another computer uses
// it), so the list is refreshed every couple of seconds.
func (s *Server) WatchLocalKeyboards() {
	if s.Escape == nil {
		return
	}
	var mu sync.Mutex
	open := map[string]bool{}
	for {
		paths, _ := filepath.Glob("/dev/input/event*")
		for _, p := range paths {
			mu.Lock()
			if open[p] {
				mu.Unlock()
				continue
			}
			f, err := os.Open(p)
			if err != nil {
				mu.Unlock()
				continue
			}
			open[p] = true
			mu.Unlock()
			go func(p string, f *os.File) {
				defer func() {
					f.Close()
					mu.Lock()
					delete(open, p)
					mu.Unlock()
				}()
				s.readKeys(f)
			}(p, f)
		}
		time.Sleep(2 * time.Second)
	}
}

// readKeys follows one input device until it goes away.
func (s *Server) readKeys(f *os.File) {
	// struct input_event: a timeval (two longs), then type, code and value.
	tv := int(unsafe.Sizeof(uintptr(0))) * 2
	ev := make([]byte, tv+8)
	down := map[uint16]bool{}
	armed := false
	var deadline time.Time
	for {
		if _, err := f.Read(ev); err != nil {
			return
		}
		typ := binary.LittleEndian.Uint16(ev[tv:])
		code := binary.LittleEndian.Uint16(ev[tv+2:])
		value := int32(binary.LittleEndian.Uint32(ev[tv+4:]))
		if typ != 1 { // EV_KEY
			continue
		}
		switch value {
		case 0:
			delete(down, code)
		case 1:
			down[code] = true
		}
		// As on a shared keyboard: act once every key is let go (or after a
		// few seconds), so no key is left "held" anywhere.
		switch {
		case !armed && s.Escape.matchKeys(down):
			armed, deadline = true, time.Now().Add(releaseWait)
		case armed && (len(down) == 0 || time.Now().After(deadline)):
			armed = false
			go s.shareAll()
		}
	}
}
