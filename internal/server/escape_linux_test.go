package server

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
	"unsafe"
)

// The escape keys pressed on one of the server machine's own keyboards
// share the kept devices again.
func TestLocalEscapeKeys(t *testing.T) {
	c, _ := ParseCombo("Ctrl+Alt+Shift+F12")
	s := &Server{Escape: c, kept: map[string]bool{"1-1": true}}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go s.readKeys(r)
	tv := int(unsafe.Sizeof(uintptr(0))) * 2
	key := func(code uint16, value int32) {
		ev := make([]byte, tv+8)
		binary.LittleEndian.PutUint16(ev[tv:], 1) // EV_KEY
		binary.LittleEndian.PutUint16(ev[tv+2:], code)
		binary.LittleEndian.PutUint32(ev[tv+4:], uint32(value))
		w.Write(ev)
	}
	key(29, 1)  // left Ctrl
	key(100, 1) // right Alt
	key(88, 1)  // F12, without Shift: nothing yet
	key(88, 0)
	time.Sleep(50 * time.Millisecond)
	if s.blocked("1-1", "x") == "" {
		t.Fatal("shared before the full combination")
	}
	key(54, 1) // right Shift
	key(88, 1)
	time.Sleep(50 * time.Millisecond)
	if s.blocked("1-1", "x") == "" {
		t.Fatal("acted while the keys were still held")
	}
	for _, k := range []uint16{88, 54, 100, 29} {
		key(k, 0)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.blocked("1-1", "x") != "" {
		if time.Now().After(deadline) {
			t.Fatal("escape keys on a local keyboard did not share the device again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Close()
}
