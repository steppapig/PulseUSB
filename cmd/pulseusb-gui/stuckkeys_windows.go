package main

import (
	"strings"
	"time"
	"unsafe"

	"pulseusb/internal/gui"
)

// When a keyboard leaves this computer while a key is down, Windows never
// sees that key come back up and treats it as held: Win+L locks the screen
// when you type an L, and so on. This finds modifiers that Windows believes
// are pressed and lets them go.

var procSendInput = user32.NewProc("SendInput")

// keyInput is Windows' INPUT holding a KEYBDINPUT (40 bytes on 64-bit).
type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

const (
	inputKeyboard = 1
	keyUp         = 0x0002
	keyExtended   = 0x0001
	maskKey       = 0xE8 // an unassigned key, pressed so that letting Win go does not open Start
)

var modifiers = []struct {
	vk       uint16
	name     string
	extended bool
}{
	{0x5B, "Win", true}, {0x5C, "Win", true},
	{0xA2, "Ctrl", false}, {0xA3, "Ctrl", true},
	{0xA4, "Alt", false}, {0xA5, "Alt", true},
	{0xA0, "Shift", false}, {0xA1, "Shift", false},
}

func sendKey(vk uint16, flags uint32) {
	in := keyInput{typ: inputKeyboard, vk: vk, flags: flags}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

// releaseStuckKeys lets go of every modifier Windows thinks is held and
// returns their names.
func releaseStuckKeys() []string {
	var released []string
	seen := map[string]bool{}
	for _, m := range modifiers {
		if r, _, _ := procGetAsyncKey.Call(uintptr(m.vk)); r&0x8000 == 0 {
			continue
		}
		if m.name == "Win" {
			sendKey(maskKey, 0)
			sendKey(maskKey, keyUp)
		}
		flags := uint32(keyUp)
		if m.extended {
			flags |= keyExtended
		}
		sendKey(m.vk, flags)
		if !seen[m.name] {
			seen[m.name] = true
			released = append(released, m.name)
		}
	}
	return released
}

// guardStuckKeys releases modifiers left held after a keyboard leaves this
// computer, and notes it in the timing list when it had to.
func guardStuckKeys(e *gui.Engine) {
	e.OnKeyboardGone = func() {
		time.Sleep(300 * time.Millisecond) // let any real key releases arrive first
		if keys := releaseStuckKeys(); len(keys) > 0 {
			e.Note("A keyboard left this computer with %s held; released", strings.Join(keys, "+"))
		}
	}
}
