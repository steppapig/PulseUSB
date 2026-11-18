package main

import (
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"

	"pulseusb/internal/gui"
)

// Global keyboard shortcuts, through Windows' RegisterHotKey. They work
// whichever program has the focus, also while the PulseUSB window is closed.

var (
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procPeekMessage      = user32.NewProc("PeekMessageW")
	procMsgWait          = user32.NewProc("MsgWaitForMultipleObjects")
	procGetAsyncKey      = user32.NewProc("GetAsyncKeyState")
)

// waitKeysUp waits until the shortcut's key and every modifier are let go
// (or a few seconds pass). Acting while they are held would unplug a shared
// keyboard mid-press, and Windows would then think Ctrl, Win and the rest
// stay pressed.
func waitKeysUp(key uint32) {
	keys := []uintptr{0x10, 0x11, 0x12, 0x5B, 0x5C, uintptr(key)} // Shift, Ctrl, Alt, both Win keys
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		held := false
		for _, k := range keys {
			if r, _, _ := procGetAsyncKey.Call(k); r&0x8000 != 0 {
				held = true
			}
		}
		if !held {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}

const (
	wmHotkey    = 0x0312
	modNoRepeat = 0x4000
	pmRemove    = 0x0001
	qsAllInput  = 0x04FF
)

// winMsg is Windows' MSG.
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

// runHotkeys keeps the shortcuts chosen in the app registered with
// Windows and carries them out when pressed.
func runHotkeys(e *gui.Engine) {
	go func() {
		// Hotkeys belong to the thread that registered them, and their
		// messages arrive in that thread's queue.
		runtime.LockOSThread()
		var m winMsg
		procPeekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 0) // creates the queue
		registered := map[uintptr]string{}                            // hotkey id -> action
		keyOf := map[uintptr]uint32{}                                 // hotkey id -> its key
		applied := "?"
		for {
			want := e.Shortcuts()
			if sig := signature(want); sig != applied {
				for id := range registered {
					procUnregisterHotKey.Call(0, id)
				}
				registered, keyOf = map[uintptr]string{}, map[uintptr]uint32{}
				errs := map[string]string{}
				targets := make([]string, 0, len(want))
				for t := range want {
					targets = append(targets, t)
				}
				sort.Strings(targets)
				for i, target := range targets {
					mods, key, canon, ok := gui.ParseShortcut(want[target])
					if !ok {
						errs[target] = "not a usable shortcut"
						continue
					}
					id := uintptr(i + 1)
					if r, _, _ := procRegisterHotKey.Call(0, id, uintptr(mods|modNoRepeat), uintptr(key)); r == 0 {
						errs[target] = canon + " is already used by Windows or another program; choose another"
						continue
					}
					registered[id], keyOf[id] = target, key
				}
				e.SetShortcutErrors(errs)
				applied = sig
			}
			// Wait up to a second for a key press, then look for changes.
			procMsgWait.Call(0, 0, 0, 1000, qsAllInput)
			for {
				r, _, _ := procPeekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove)
				if r == 0 {
					break
				}
				if m.message == wmHotkey {
					if target, ok := registered[m.wParam]; ok {
						key := keyOf[m.wParam]
						go func() {
							waitKeysUp(key)
							e.Trigger(target)
						}()
					}
				}
			}
		}
	}()
}

func signature(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}
