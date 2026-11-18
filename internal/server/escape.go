package server

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// The escape keys work like the hotkey of a KVM switch. Pressed on a
// keyboard that is being used by another computer, they give every device
// back to the server machine (the server sees that keyboard's key presses
// as it passes them on). Pressed on a keyboard of the server machine itself,
// they let other computers have the devices again.

// Combo is a key combination, both as a USB keyboard reports it and as
// Linux reports it for a local keyboard.
type Combo struct {
	text    string
	hidMods byte     // modifier bits that must be held (left or right)
	hidKey  byte     // USB usage of the key
	evMods  []uint16 // one entry per modifier: its left code (right = table)
	evKey   uint16   // Linux key code of the key
}

func (c *Combo) String() string { return c.text }

type keyCodes struct {
	hid byte
	ev  uint16
}

var comboKeys = func() map[string]keyCodes {
	m := map[string]keyCodes{
		"End": {0x4d, 107}, "Home": {0x4a, 102}, "Insert": {0x49, 110}, "Delete": {0x4c, 111},
		"PageUp": {0x4b, 104}, "PageDown": {0x4e, 109}, "Pause": {0x48, 119}, "ScrollLock": {0x47, 70},
		"Space": {0x2c, 57}, "Backspace": {0x2a, 14}, "Escape": {0x29, 1},
		"F11": {0x44, 87}, "F12": {0x45, 88},
	}
	for i := 1; i <= 10; i++ {
		m[fmt.Sprintf("F%d", i)] = keyCodes{byte(0x3a + i - 1), uint16(59 + i - 1)}
	}
	letters := map[rune]uint16{'Q': 16, 'W': 17, 'E': 18, 'R': 19, 'T': 20, 'Y': 21, 'U': 22, 'I': 23, 'O': 24, 'P': 25,
		'A': 30, 'S': 31, 'D': 32, 'F': 33, 'G': 34, 'H': 35, 'J': 36, 'K': 37, 'L': 38,
		'Z': 44, 'X': 45, 'C': 46, 'V': 47, 'B': 48, 'N': 49, 'M': 50}
	for c, ev := range letters {
		m[string(c)] = keyCodes{byte(0x04 + c - 'A'), ev}
	}
	for d := 1; d <= 9; d++ {
		m[fmt.Sprint(d)] = keyCodes{byte(0x1e + d - 1), uint16(2 + d - 1)}
	}
	m["0"] = keyCodes{0x27, 11}
	return m
}()

// Modifiers: USB bits for left and right, Linux codes for left and right.
var comboMods = map[string]struct {
	hid     byte
	ev, ev2 uint16
}{
	"ctrl": {0x11, 29, 97}, "shift": {0x22, 42, 54}, "alt": {0x44, 56, 100}, "win": {0x88, 125, 126},
}

// ParseCombo reads a combination such as "Ctrl+Alt+Shift+F12".
func ParseCombo(s string) (*Combo, error) {
	parts := strings.Split(s, "+")
	if len(parts) < 2 {
		return nil, fmt.Errorf("%q: give modifiers and a key, for example Ctrl+Alt+Shift+F12", s)
	}
	c := &Combo{}
	var names []string
	for _, p := range parts[:len(parts)-1] {
		name := strings.ToLower(strings.TrimSpace(p))
		if name == "control" {
			name = "ctrl"
		}
		m, ok := comboMods[name]
		if !ok {
			return nil, fmt.Errorf("%q: unknown modifier %q (use Ctrl, Alt, Shift, Win)", s, p)
		}
		c.hidMods |= m.hid
		c.evMods = append(c.evMods, m.ev)
		names = append(names, strings.ToUpper(name[:1])+name[1:])
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	for k, v := range comboKeys {
		if strings.EqualFold(k, key) {
			c.hidKey, c.evKey, key = v.hid, v.ev, k
		}
	}
	if c.hidKey == 0 {
		return nil, fmt.Errorf("%q: unknown key %q", s, parts[len(parts)-1])
	}
	c.text = strings.Join(append(names, key), "+")
	return c, nil
}

// matchReport reports whether a USB keyboard report shows the combination
// held. Boot-format reports are understood: 8 bytes (modifiers, reserved,
// six keys), or 9 with a report number in front.
func (c *Combo) matchReport(r []byte) bool {
	switch len(r) {
	case 8:
	case 9:
		r = r[1:]
	default:
		return false
	}
	for _, m := range comboMods {
		if c.hidMods&m.hid != 0 && r[0]&m.hid == 0 {
			return false // a required modifier is not held on either side
		}
	}
	for _, k := range r[2:8] {
		if k == c.hidKey {
			return true
		}
	}
	return false
}

// matchKeys reports whether the set of Linux key codes held shows the
// combination.
func (c *Combo) matchKeys(down map[uint16]bool) bool {
	if !down[c.evKey] {
		return false
	}
	for _, left := range c.evMods {
		right := left
		for _, m := range comboMods {
			if m.ev == left {
				right = m.ev2
			}
		}
		if !down[left] && !down[right] {
			return false
		}
	}
	return true
}

// KeyboardEndpoints is implemented by devices that can tell which of their
// IN endpoints carry keyboard reports.
type KeyboardEndpoints interface {
	KeyboardEndpoints() map[uint8]bool
}

// escapeToHost gives every device back to the server machine, as if
// "pulseusb-server release all" had been run there.
func (s *Server) escapeToHost() {
	s.logf("escape keys (%s) pressed: every device is back on this machine; press them again here to share", s.Escape)
	s.keepAll()
}

// keepAll keeps every device on the server machine.
func (s *Server) keepAll() {
	var all []string
	if devs, err := s.Backend.List(); err == nil {
		for _, d := range devs {
			all = append(all, d.BusID)
		}
	}
	s.dmu.Lock()
	if s.kept == nil {
		s.kept, s.reserved = map[string]bool{}, map[string]reservation{}
	}
	for b := range s.held {
		all = append(all, b)
	}
	for _, b := range all {
		s.kept[b] = true
	}
	s.dmu.Unlock()
	for _, b := range all {
		s.drop(b, "")
	}
}

// shareAll lets other computers have every device again.
func (s *Server) shareAll() {
	s.dmu.Lock()
	n := len(s.kept)
	s.kept = map[string]bool{}
	s.dmu.Unlock()
	if n > 0 {
		s.logf("escape keys (%s) pressed on this machine: devices are shared again", s.Escape)
	}
}

// escapeWatch tracks the escape keys on one keyboard passing to a client.
type escapeWatch struct {
	mu    sync.Mutex
	armed bool
	timer *time.Timer
}

// releaseWait is how long the escape keys wait for every key to be let go
// before acting anyway.
const releaseWait = 3 * time.Second

// reportIdle reports whether a boot-format keyboard report shows no key or
// modifier held.
func reportIdle(r []byte) bool {
	switch len(r) {
	case 8:
	case 9:
		r = r[1:]
	default:
		return false
	}
	for i, b := range r {
		if i != 1 && b != 0 { // byte 1 is reserved
			return false
		}
	}
	return true
}

func (s *Server) checkReport(w *escapeWatch, r []byte) {
	if s.Escape == nil {
		return
	}
	// Act once every key has been let go, not while they are held: the
	// key releases must reach the computer using the keyboard first, or it
	// keeps thinking Ctrl, Alt and the rest are pressed.
	fire := func() { go s.escapeToHost() } // not inline: it waits for this session to end
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case !w.armed && s.Escape.matchReport(r):
		w.armed = true
		w.timer = time.AfterFunc(releaseWait, func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.armed {
				w.armed = false
				fire()
			}
		})
	case w.armed && reportIdle(r):
		w.armed = false
		w.timer.Stop()
		fire()
	}
}
