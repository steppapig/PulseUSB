package gui

import (
	"strings"
)

// Keyboard shortcuts are stored as text such as "Ctrl+Alt+R": modifiers
// (Ctrl, Alt, Shift, Win) and one key. They are global, so at least one of
// Ctrl, Alt or Win is required, or plain typing would trigger them.

// Modifier bits, as Windows' RegisterHotKey expects them.
const (
	ModAlt   = 0x1
	ModCtrl  = 0x2
	ModShift = 0x4
	ModWin   = 0x8
)

var shortcutKeys = func() map[string]uint32 {
	m := map[string]uint32{
		"Space": 0x20, "PageUp": 0x21, "PageDown": 0x22, "End": 0x23, "Home": 0x24,
		"Insert": 0x2D, "Delete": 0x2E, "Pause": 0x13,
	}
	for c := 'A'; c <= 'Z'; c++ {
		m[string(c)] = uint32(c)
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = uint32(c)
		m["Num"+string(c)] = 0x60 + uint32(c-'0')
	}
	for i := 1; i <= 24; i++ {
		m["F"+itoa(i)] = 0x6F + uint32(i)
	}
	return m
}()

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// ParseShortcut reads a shortcut and returns its modifier bits, its Windows
// virtual-key code and its canonical spelling.
func ParseShortcut(s string) (mods, key uint32, canon string, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), "+")
	if len(parts) < 2 {
		return 0, 0, "", false
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	for k, v := range shortcutKeys {
		if strings.EqualFold(k, name) {
			key, name = v, k
		}
	}
	if key == 0 {
		return 0, 0, "", false
	}
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control":
			mods |= ModCtrl
		case "alt":
			mods |= ModAlt
		case "shift":
			mods |= ModShift
		case "win", "super", "meta":
			mods |= ModWin
		default:
			return 0, 0, "", false
		}
	}
	if mods&(ModCtrl|ModAlt|ModWin) == 0 {
		return 0, 0, "", false
	}
	var b []string
	for _, m := range []struct {
		bit  uint32
		name string
	}{{ModCtrl, "Ctrl"}, {ModAlt, "Alt"}, {ModShift, "Shift"}, {ModWin, "Win"}} {
		if mods&m.bit != 0 {
			b = append(b, m.name)
		}
	}
	return mods, key, strings.Join(append(b, name), "+"), true
}
