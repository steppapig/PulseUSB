package gui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pulseusb/internal/attach"
	"pulseusb/internal/client"
	"pulseusb/internal/discover"
	"pulseusb/internal/proto"
)

type fake struct {
	kinds     map[string]string
	others    map[string]client.Holder // devices other computers hold
	released  []string
	queries   int // how often the driver was asked what is attached
	searches  int // how often the network was searched
	mu        sync.Mutex
	down      bool
	have      map[string]int
	next      int
	detachs   int
	found     []discover.Server
	streaming map[string]bool // mode each device was last attached in
}

func newFake() *fake { return &fake{have: map[string]int{}, streaming: map[string]bool{}} }

// Holders: what other computers hold, set by a test.
func (f *fake) Holders(string) (map[string]client.Holder, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]client.Holder{}
	for k, v := range f.others {
		out[k] = v
	}
	return out, "192.0.2.1", nil // this computer, as the server sees it
}

func (f *fake) Release(host, busid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, busid)
	delete(f.others, busid)
	return nil
}

func (f *fake) Names(string) map[string]string { return map[string]string{"1-1": "Acme Wheel"} }
func (f *fake) Kinds(string) map[string]string { return f.kinds }

func (f *fake) Discover() []discover.Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches++
	return f.found
}

func (f *fake) List(host string) ([]*proto.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	return []*proto.Device{
		{BusID: "1-1", VendorID: 0x1234, ProductID: 1, Interfaces: []proto.Interface{{Class: 3}}},
		{BusID: "1-2", VendorID: 0x046d, ProductID: 0xc52b, Interfaces: []proto.Interface{{Class: 8}}},
	}, nil
}

func (f *fake) Attached() (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	out := map[string]int{}
	for k, v := range f.have {
		out[k] = v
	}
	return out, nil
}

func (f *fake) Attach(host, busid string, once, streaming bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.have[attach.Key(host, busid)] = f.next
	f.streaming[busid] = streaming
	return nil
}

func (f *fake) Detach(port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detachs++
	for k, v := range f.have {
		if v == port {
			delete(f.have, k)
		}
	}
	return nil
}

type fakeAuto struct {
	on, min, bg bool
	bgPath      string
	target      string // where the program is installed now
	prepared    int
}

func (a *fakeAuto) BackgroundTarget() string { return a.target }
func (a *fakeAuto) PrepareBackground(string) error {
	a.prepared++
	return nil
}

func (a *fakeAuto) Supported() bool           { return true }
func (a *fakeAuto) Enabled() bool             { return a.on }
func (a *fakeAuto) BackgroundSupported() bool { return true }
func (a *fakeAuto) Set(on, min bool) error {
	a.on, a.min = on, min
	return nil
}
func (a *fakeAuto) SetBackground(on bool, path string) error {
	a.bg, a.bgPath = on, path
	return nil
}

func status(s State) map[string]string {
	m := map[string]string{}
	for _, sv := range s.Servers {
		for _, d := range sv.Devices {
			m[d.BusID] = d.Status
		}
	}
	return m
}

func TestEngine(t *testing.T) {
	f := newFake()
	auto := &fakeAuto{target: `C:\Apps\PulseUSB.exe`}
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	e := &Engine{Backend: f, Autostart: auto, Path: path}
	e.Load()
	const host = "rig:3240"

	e.step()
	if s := e.State(); len(s.Servers) != 0 {
		t.Fatalf("no server: %+v", s)
	}
	// On the very first run the only server on the network is connected,
	// but none of its devices are touched until asked.
	f.found = []discover.Server{{Name: "rig", Addr: host}}
	e.step()
	e.step()
	s := e.State()
	if len(s.Servers) != 1 || !s.Servers[0].OK || s.Servers[0].Name != "rig" || len(s.Found) != 0 || len(f.have) != 0 {
		t.Fatalf("first run: %+v", s)
	}
	d := s.Servers[0].Devices
	if st := status(s); st["1-1"] != "disconnected" || st["1-2"] != "disconnected" || d[0].Name != "Acme Wheel" ||
		d[1].Name != "Logitech, Inc. Unifying Receiver" || d[0].Kind != "Game controller" || d[1].ID != "046d:c52b" {
		t.Fatalf("rows: %+v", d)
	}

	// Connect is for this session; a dropped device comes back.
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.step()
	if st := status(e.State()); st["1-1"] != "connected" || st["1-2"] != "disconnected" || len(f.have) != 1 {
		t.Fatalf("connect: %v", st)
	}
	f.have = map[string]int{}
	e.step()
	if len(f.have) != 1 {
		t.Fatal("dropped device not re-attached")
	}

	// Auto-connect attaches now and is remembered; storage defaults to bulk mode.
	e.SetAuto(host, "1-2", true)
	e.step()
	if st := status(e.State()); st["1-2"] != "connected" || !f.streaming["1-2"] || f.streaming["1-1"] {
		t.Fatalf("auto: %v %v", st, f.streaming)
	}

	// Disconnect wins over auto-connect until the app restarts, and the row
	// says so.
	e.SetConnected(host, "1-2", false)
	e.settle()
	if d := e.State().Servers[0].Devices[1]; !d.Paused {
		t.Fatalf("not marked paused at once: %+v", d)
	}
	e.step()
	e.step()
	if d := e.State().Servers[0].Devices; !d[1].Paused || d[0].Paused {
		t.Fatalf("paused flags: %+v", d)
	}
	if st := status(e.State()); st["1-2"] != "disconnected" || len(f.have) != 1 {
		t.Fatalf("disconnect: %v %v", st, f.have)
	}

	// A mode change reconnects the device in the new mode.
	e.SetMode(host, "1-1", "zero-copy")
	e.settle()
	e.step()
	s = e.State()
	if st := status(s); st["1-1"] != "connected" || !f.streaming["1-1"] || s.Servers[0].Devices[0].Mode != "zero-copy" {
		t.Fatalf("mode: %+v", s.Servers[0].Devices)
	}

	// Unreachable server: reported, devices the driver still holds stay shown.
	f.down = true
	e.step()
	s = e.State()
	if st := status(s); s.Servers[0].OK || s.Servers[0].Error == "" || st["1-1"] != "connected" || st["1-2"] != "disconnected" {
		t.Fatalf("server down: %+v", s)
	}
	const host10 = "10.0.0.9:3240"
	// ... and the same server at a new address is followed there.
	f.found = []discover.Server{{Name: "rig", Addr: "10.0.0.9:3240"}, {Name: "other", Addr: "10.0.0.7:3240"}}
	e.step()
	if c := e.Config(); c.Servers[0].Host != "10.0.0.9:3240" {
		t.Fatalf("did not follow server: %+v", c)
	}
	if s := e.State(); len(s.Found) != 1 || s.Found[0].Name != "other" {
		t.Fatalf("found list: %+v", s.Found)
	}
	f.down = false

	// Startup settings; background start is told which settings to follow.
	if s := e.State(); !s.MinimizeToTray || s.StartMinimized {
		t.Fatalf("defaults: %+v", s)
	}
	if err := e.SetStartup(Startup{Autostart: true, Minimized: true, Background: true}); err != nil ||
		!auto.on || !auto.min || !auto.bg || auto.bgPath != path || e.State().MinimizeToTray {
		t.Fatalf("startup: %v %+v", err, auto)
	}

	// The tray menu mirrors servers and devices, and only changes signature
	// when something visible changes.
	e.step()
	menu, sig := e.TrayMenu()
	if sv := menu.Servers; len(sv) != 1 || sv[0].Title != "rig" || len(sv[0].Devices) != 2 ||
		sv[0].Devices[0].Title != "Acme Wheel" || !sv[0].Devices[0].Connected || sv[0].Devices[1].Connected {
		t.Fatalf("tray menu: %+v", menu)
	}
	if _, again := e.TrayMenu(); again != sig {
		t.Fatal("tray signature changed without a change")
	}
	e.SetConnected(menu.Servers[0].Devices[1].Host, menu.Servers[0].Devices[1].BusID, true)
	e.settle()
	e.step()
	if m2, sig2 := e.TrayMenu(); sig2 == sig || !m2.Servers[0].Devices[1].Connected {
		t.Fatalf("tray menu after connect: %+v", m2)
	}

	// Aliases replace the name wherever the device is shown; groups connect
	// and disconnect their members together.
	e.SetAlias(host10, "1-1", "  My   wheel  ")
	e.SetGroup(host10, "1-1", "Sim rig")
	e.SetGroup(host10, "1-2", "Sim rig")
	e.step()
	s = e.State()
	if d := s.Servers[0].Devices; d[0].Alias != "My wheel" || d[0].Name != "Acme Wheel" || d[1].Group != "Sim rig" ||
		len(s.Groups) != 1 || s.Groups[0] != (GroupState{Name: "Sim rig", Total: 2, Connected: 2}) || len(s.GroupNames) != 1 {
		t.Fatalf("alias/group: %+v %+v", d, s.Groups)
	}
	if m3, _ := e.TrayMenu(); m3.Servers[0].Devices[0].Title != "My wheel" || len(m3.Groups) != 1 {
		t.Fatalf("tray with alias: %+v", m3)
	}
	e.SetGroupConnected("Sim rig", false)
	e.settle()
	e.step()
	held := 0
	for k := range f.have {
		if strings.HasPrefix(k, host10) {
			held++
		}
	}
	if s = e.State(); s.Groups[0].Connected != 0 || held != 0 {
		t.Fatalf("group disconnect: %+v %v", s.Groups, f.have)
	}
	e.SetGroupConnected("Sim rig", true)
	e.settle()
	e.step()
	if s = e.State(); s.Groups[0].Connected != 2 {
		t.Fatalf("group connect: %+v", s.Groups)
	}
	e.SetAlias(host10, "1-1", "")
	e.SetGroup(host10, "1-1", "")
	e.step()
	if s = e.State(); s.Servers[0].Devices[0].Alias != "" || s.Groups[0].Total != 1 {
		t.Fatalf("clear alias/group: %+v", s)
	}
	// A hidden device drops out of the tray and its group, and loses
	// auto-connect; unhiding brings it back.
	e.SetAuto(host10, "1-2", true)
	e.SetHidden(host10, "1-2", true)
	e.step()
	s = e.State()
	if d := s.Servers[0].Devices[1]; !d.Hidden || d.Auto || s.Groups[0].Total != 0 {
		t.Fatalf("hide: %+v %+v", d, s.Groups)
	}
	if m, _ := e.TrayMenu(); len(m.Servers[0].Devices) != 1 || len(m.Groups) != 0 {
		t.Fatalf("tray lists hidden device: %+v", m)
	}
	e.SetHidden(host10, "1-2", false)
	e.step()
	if s = e.State(); s.Servers[0].Devices[1].Hidden || s.Groups[0].Total != 1 {
		t.Fatalf("unhide: %+v", s)
	}
	e.SetAuto(host10, "1-2", true)
	// Renaming a group carries its devices along.
	e.RenameGroup("Sim rig", "  Racing  ")
	if s = e.State(); len(s.Groups) != 1 || s.Groups[0].Name != "Racing" || s.Servers[0].Devices[1].Group != "Racing" {
		t.Fatalf("rename group: %+v", s.Groups)
	}
	e.RenameGroup("Racing", "Sim rig")
	e.SetTheme("light")
	if e.State().Theme != "light" || e.Config().Theme != "light" {
		t.Fatal("theme not stored")
	}
	e.SetTheme("system")
	if e.State().Theme != "" {
		t.Fatal("unknown theme should mean: follow the system")
	}
	// Deleting a group ungroups its devices and leaves them connected.
	e.DeleteGroup("Sim rig")
	if s = e.State(); len(s.Groups) != 0 || len(s.GroupNames) != 0 || s.Servers[0].Devices[1].Group != "" ||
		s.Servers[0].Devices[1].Status != "connected" {
		t.Fatalf("delete group: %+v", s)
	}
	if m, _ := e.TrayMenu(); len(m.Groups) != 0 {
		t.Fatalf("tray still lists deleted group: %+v", m.Groups)
	}
	e.SetConnected(host10, "1-1", true)
	e.settle()
	e.SetConnected(host10, "1-2", false)
	e.settle()
	e.step()

	// Start-at-boot remembers which program it was set up for. When the
	// program is reinstalled somewhere else it is reported as broken, and
	// repairing sets it up for the new place.
	if e.BackgroundStale() || e.Config().BackgroundExe != `C:\Apps\PulseUSB.exe` {
		t.Fatalf("fresh setup reported stale: %+v", e.Config())
	}
	auto.target = `D:\Tools\PulseUSB\PulseUSB.exe`
	if !e.BackgroundStale() || !e.State().BackgroundStale {
		t.Fatal("moved program not detected")
	}
	e.PrepareBackground()
	if err := e.RepairBackground(); err != nil || e.BackgroundStale() || auto.prepared != 1 || !auto.bg {
		t.Fatalf("repair: %v stale=%v", err, e.BackgroundStale())
	}

	// After a restart: settings are back, auto-connect devices attach (the
	// earlier Disconnect no longer holds), session connects do not.
	f.have = map[string]int{}
	e2 := &Engine{Backend: f, Autostart: auto, Path: path}
	e2.Load()
	e2.step()
	s = e2.State()
	if st := status(s); len(s.Servers) != 1 || s.Servers[0].Host != "10.0.0.9:3240" || st["1-2"] != "connected" ||
		st["1-1"] != "disconnected" || !s.StartMinimized || !s.Background || !s.Servers[0].Devices[1].Auto {
		t.Fatalf("after restart: %+v", s)
	}

	// The boot-time instance follows the same file but never writes it.
	before, _ := os.ReadFile(path)
	bg := &Engine{Backend: f, Path: path, ReadOnly: true, Kind: "background"}
	bg.Load()
	bg.step()
	bg.AddServer("elsewhere", "")
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("read-only engine wrote the settings file")
	}

	// Disconnecting a server unplugs its devices and removes it; it is then
	// offered again as found, and not reconnected by itself.
	e2.RemoveServer("10.0.0.9:3240")
	e2.step()
	s = e2.State()
	if len(s.Servers) != 0 || len(f.have) != 0 || len(s.Found) != 2 {
		t.Fatalf("remove server: %+v %v", s, f.have)
	}
}

// A click is carried out on its own, even while the background cycle is
// stuck on a server that does not answer.
func TestClickNotQueued(t *testing.T) {
	f := newFake()
	slow := &slowList{fake: f, gate: make(chan struct{})}
	e := &Engine{Backend: slow}
	e.Load()
	const host = "rig:3240"
	e.AddServer(host, "rig")
	e.step()               // learn the devices while the server answers
	slow.block.Store(true) // from now on, listing hangs until released
	go e.cycle(false)      // a background cycle gets stuck on it
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	e.SetConnected(host, "1-1", true)
	deadline := time.Now().Add(2 * time.Second)
	for status(e.State())["1-1"] != "connected" {
		if time.Now().After(deadline) {
			t.Fatalf("click waited for the stuck cycle: %v", status(e.State()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("connect took %v", took)
	}
	if tm := e.State().Timings; len(tm) == 0 || !strings.Contains(tm[0], "Connect Acme Wheel") {
		t.Fatalf("timing not noted: %v", tm)
	}
	// The stuck cycle, once it finishes, must not undo the click or plug
	// the device in a second time.
	n := f.next
	close(slow.gate)
	time.Sleep(100 * time.Millisecond)
	e.step()
	if st := status(e.State()); st["1-1"] != "connected" || f.next != n {
		t.Fatalf("after the slow cycle: %v (attached %d more times)", st, f.next-n)
	}
	// Group members are handled side by side.
	slow.block.Store(false)
	e.SetGroup(host, "1-1", "G")
	e.SetGroup(host, "1-2", "G")
	e.SetConnected(host, "1-1", false)
	time.Sleep(50 * time.Millisecond)
	e.step()
	e.SetGroupConnected("G", true)
	time.Sleep(100 * time.Millisecond)
	if st := status(e.State()); st["1-1"] != "connected" || st["1-2"] != "connected" {
		t.Fatalf("group connect: %v", st)
	}
}

// slowList makes listing hang while block is set, until gate is closed.
type slowList struct {
	*fake
	block atomic.Bool
	gate  chan struct{}
}

func (s *slowList) List(host string) ([]*proto.Device, error) {
	if s.block.Load() {
		<-s.gate
	}
	return s.fake.List(host)
}

// A click is acted on at once: the cycle it starts neither searches the
// network nor asks the driver what is attached first, and the window is
// told the moment the result is in.
func TestFastCycle(t *testing.T) {
	f := newFake()
	e := &Engine{Backend: f}
	e.Load()
	const host = "rig:3240"
	e.AddServer(host, "rig")
	e.step()
	q, s := f.queries, f.searches

	rev := e.State().Rev
	woke := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		e.Wait(rev, 5*time.Second)
		woke <- time.Since(start)
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter settle
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.cycle(true) // confirms the click with the driver
	if st := status(e.State()); st["1-1"] != "connected" || len(f.have) != 1 {
		t.Fatalf("fast cycle did not connect: %v", st)
	}
	q, s = f.queries, f.searches
	e.cycle(true) // an ordinary fast cycle asks nobody
	if f.queries != q || f.searches != s {
		t.Fatalf("fast cycle asked the driver %d and searched %d extra times", f.queries-q, f.searches-s)
	}
	if d := <-woke; d > time.Second {
		t.Fatalf("window was told only after %v", d)
	}
	if e.State().Rev == rev {
		t.Fatal("revision did not move")
	}
	// A second fast cycle must not attach the same device twice.
	n := f.next
	e.cycle(true)
	if f.next != n {
		t.Fatal("device attached twice")
	}
	// Disconnecting is remembered by the cache too, and a full cycle agrees.
	e.SetConnected(host, "1-1", false)
	e.settle()
	e.cycle(true)
	e.step()
	if st := status(e.State()); st["1-1"] != "disconnected" || len(f.have) != 0 {
		t.Fatalf("after disconnect: %v %v", st, f.have)
	}
}

// A device another computer uses is shown as such, never grabbed by
// auto-connect, and can be taken over, alone or with its group.
func TestElsewhereAndTakeOver(t *testing.T) {
	f := newFake()
	f.others = map[string]client.Holder{"1-1": {Peer: "192.0.2.7"}, "1-2": {Kept: true}}
	e := &Engine{Backend: f}
	e.Load()
	const host = "rig:3240"
	e.AddServer(host, "rig")
	e.step()
	e.SetAuto(host, "1-1", true)
	e.step()
	s := e.State()
	d := s.Servers[0].Devices
	if d[0].Status != "elsewhere" || d[0].Elsewhere == "" || d[1].Status != "kept" || len(f.have) != 0 {
		t.Fatalf("in use elsewhere: %+v (attached %v)", d, f.have)
	}
	e.SetGroup(host, "1-1", "Rig")
	e.step()
	if g := e.State().Groups[0]; g.Elsewhere != 1 {
		t.Fatalf("group: %+v", g)
	}
	// The group shortcut takes the group over.
	if err := e.SetShortcut("group:Rig", "ctrl+alt+r"); err != nil {
		t.Fatal(err)
	}
	if c := e.Shortcuts()["group:Rig"]; c != "Ctrl+Alt+R" {
		t.Fatalf("shortcut stored as %q", c)
	}
	e.Trigger("group:Rig")
	e.settle()
	e.step()
	if st := status(e.State()); st["1-1"] != "connected" || len(f.released) != 1 || f.released[0] != "1-1" {
		t.Fatalf("take over: %v released %v", st, f.released)
	}
	// Pressed again: the group goes off.
	e.Trigger("group:Rig")
	e.settle()
	e.step()
	if st := status(e.State()); st["1-1"] != "disconnected" {
		t.Fatalf("group off: %v", st)
	}
	// Disconnect everything.
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.Trigger("all:off")
	e.settle()
	if len(f.have) != 0 {
		t.Fatalf("still attached: %v", f.have)
	}
	// Shortcuts follow a renamed group, go with a deleted one, and are
	// unique.
	e.RenameGroup("Rig", "Sim")
	if e.Shortcuts()["group:Sim"] != "Ctrl+Alt+R" {
		t.Fatal("shortcut did not follow the rename")
	}
	e.SetShortcut("all:off", "Ctrl+Alt+R")
	if sc := e.Shortcuts(); sc["group:Sim"] != "" || sc["all:off"] != "Ctrl+Alt+R" {
		t.Fatalf("shortcut not moved: %v", sc)
	}
	if err := e.SetShortcut("all:off", "Shift+R"); err == nil {
		t.Fatal("a shortcut without Ctrl, Alt or Win was accepted")
	}
}

// Shortcuts act on what the driver says, not on what the window shows;
// and the server listing this computer as a holder is not "another computer".
func TestShortcutUsesRealState(t *testing.T) {
	f := newFake()
	e := &Engine{Backend: f}
	e.Load()
	const host = "rig:3240"
	e.AddServer(host, "rig")
	e.step()
	e.SetGroup(host, "1-1", "Rig")
	e.SetGroup(host, "1-2", "Rig")
	// Both are attached, but the app's memory says they are not.
	f.have[attach.Key(host, "1-1")], f.have[attach.Key(host, "1-2")] = 7, 8
	e.mu.Lock()
	for i := range e.servers[host].devices {
		e.servers[host].devices[i].Status = "disconnected"
	}
	e.mu.Unlock()
	e.Trigger("group:Rig")
	e.settle()
	if len(f.have) != 0 {
		t.Fatalf("shortcut turned the group on instead of off: %v", f.have)
	}
	// After a click, the next cycle checks with the driver.
	q := f.queries
	e.SetConnected(host, "1-2", true)
	e.settle()
	e.cycle(true)
	if f.queries == q {
		t.Fatal("the driver was not asked after a click")
	}

	// Held by this computer according to the server, though the driver's
	// list lags behind: shown as ours, not as another computer's.
	f2 := newFake()
	f2.others = map[string]client.Holder{"1-1": {Peer: "192.0.2.1"}, "1-2": {Peer: "192.0.2.9"}}
	e2 := &Engine{Backend: f2}
	e2.Load()
	e2.AddServer(host, "rig")
	e2.step()
	d := e2.State().Servers[0].Devices
	if d[0].Status != "connected" || d[0].Elsewhere != "" || d[1].Status != "elsewhere" {
		t.Fatalf("own device vs another computer's: %+v", d)
	}
}

// When a keyboard leaves this computer, by a click here or taken by
// another, the app gets the chance to release keys left "held".
func TestKeyboardGone(t *testing.T) {
	f := newFake()
	f.kinds = map[string]string{"1-1": "Keyboard"}
	e := &Engine{Backend: f}
	calls := make(chan struct{}, 4)
	e.OnKeyboardGone = func() { calls <- struct{}{} }
	e.Load()
	const host = "rig:3240"
	e.AddServer(host, "rig")
	e.step()
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.step()
	e.SetConnected(host, "1-1", false) // unplugged by a click
	e.settle()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("not told when a keyboard was disconnected here")
	}
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.step()
	f.mu.Lock()
	f.have = map[string]int{} // taken by another computer
	f.mu.Unlock()
	e.step()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("not told when a keyboard was taken away")
	}
	// A game controller leaving is no reason.
	f.kinds = map[string]string{"1-1": "Game controller"}
	e.rtNamesReset(host)
	e.SetConnected(host, "1-1", true)
	e.settle()
	e.step()
	e.SetConnected(host, "1-1", false)
	e.settle()
	select {
	case <-calls:
		t.Fatal("told about a game controller")
	case <-time.After(200 * time.Millisecond):
	}
}

// A DualSense has an audio interface for its headphone jack; it must still
// be called a game controller.
func TestKindOf(t *testing.T) {
	ds := &proto.Device{VendorID: 0x054c, ProductID: 0x0ce6, Interfaces: []proto.Interface{{Class: 1}, {Class: 1}, {Class: 3}}}
	if k := kindOf(ds, "Game controller", ""); k != "Game controller" {
		t.Fatalf("from the server: %q", k)
	}
	if k := kindOf(ds, "", "Sony Interactive Entertainment DualSense Wireless Controller"); k != "Game controller" {
		t.Fatalf("from the name: %q", k)
	}
	headset := &proto.Device{Interfaces: []proto.Interface{{Class: 1}, {Class: 3}}}
	if k := kindOf(headset, "", "Acme USB Headset"); k != "Audio" {
		t.Fatalf("headset: %q", k)
	}
}

// Sound devices default to the zero-copy ("bulk data") mode; game
// controllers with audio inside keep low latency.
func TestAudioDefaultsToBulk(t *testing.T) {
	f := newFake()
	e := &Engine{Backend: f}
	e.Load()
	e.AddServer("rig", "rig")
	e.step()
	e.mu.Lock()
	rt := e.servers["rig:3240"]
	e.mu.Unlock()
	if rt == nil {
		t.Fatal("no server")
	}
	mic := &proto.Device{BusID: "1-9", VendorID: 0x1234, ProductID: 9, Interfaces: []proto.Interface{{Class: 1}, {Class: 1}}}
	pad := &proto.Device{BusID: "1-8", VendorID: 0x054c, ProductID: 0x0ce6, Interfaces: []proto.Interface{{Class: 1}, {Class: 3}}}
	if k := kindOf(mic, "", "Acme USB Microphone"); k != "Audio" || !(attach.Streaming(mic) || k == "Audio") {
		t.Fatal("microphone should default to bulk data")
	}
	if k := kindOf(pad, "Game controller", ""); attach.Streaming(pad) || k == "Audio" {
		t.Fatal("a DualSense should keep low latency")
	}
}

func TestParseShortcut(t *testing.T) {
	for in, want := range map[string]string{
		"ctrl+alt+r": "Ctrl+Alt+R", "Alt+Shift+F5": "Alt+Shift+F5", "Win+Ctrl+num3": "Ctrl+Win+Num3",
		"Ctrl+Alt+Delete": "Ctrl+Alt+Delete", "Ctrl+0": "Ctrl+0",
	} {
		if _, _, got, ok := ParseShortcut(in); !ok || got != want {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"R", "Shift+R", "Ctrl+", "Ctrl+Alt+Banana", "Hyper+R"} {
		if _, _, _, ok := ParseShortcut(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if mods, key, _, _ := ParseShortcut("Ctrl+Alt+F1"); mods != ModCtrl|ModAlt || key != 0x70 {
		t.Errorf("F1: %x %x", mods, key)
	}
}

func TestMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"host":"192.168.1.50","name":"simrig","ignore":["1-7"],"startMinimized":true}`), 0o644)
	e := &Engine{Backend: newFake(), Path: path}
	e.Load()
	c := e.Config()
	if len(c.Servers) != 1 || c.Servers[0].Host != "192.168.1.50:3240" || c.Servers[0].Name != "simrig" || !c.StartMinimized || !c.Initialized {
		t.Fatalf("%+v", c)
	}
}

func TestHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	quit := make(chan struct{})
	e := &Engine{Backend: newFake(), Autostart: &fakeAuto{}, Path: path, Kind: "gui", OnQuit: func() { close(quit) }}
	e.Load()
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	do := func(method, path, token, host, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("X-Token", token)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, html := do("GET", "/", "", "", "")
	m := regexp.MustCompile(`const token = "([0-9a-f]{32})"`).FindStringSubmatch(html)
	if code != 200 || m == nil {
		t.Fatalf("page: %d", code)
	}
	token := m[1]
	if onDisk, _ := os.ReadFile(TokenPath(path)); string(onDisk) != token {
		t.Fatal("token file does not match")
	}
	if code, _ := do("POST", "/api/server/add", "wrong", "", `{"host":"evil"}`); code != 403 {
		t.Fatalf("missing token accepted: %d", code)
	}
	if code, _ := do("GET", "/", "", "evil.example:80", ""); code != 403 {
		t.Fatalf("foreign Host header accepted: %d", code)
	}
	if code, body := do("GET", "/api/version", "", "", ""); code != 200 || !regexp.MustCompile(`"kind":"gui"`).MatchString(body) {
		t.Fatalf("version: %d %s", code, body)
	}
	code, body := do("POST", "/api/server/add", token, "", `{"host":" rig ","name":"rig"}`)
	var s State
	if json.Unmarshal([]byte(body), &s) != nil || code != 200 || len(s.Servers) != 1 || s.Servers[0].Host != "rig:3240" {
		t.Fatalf("add server: %d %s", code, body)
	}
	e.step()
	if code, _ = do("POST", "/api/device/connect", token, "", `{"host":"rig:3240","busid":"1-1","on":true}`); code != 200 {
		t.Fatalf("connect: %d", code)
	}
	e.settle()
	e.step()
	if st := status(e.State()); st["1-1"] != "connected" {
		t.Fatalf("connect via API: %v", st)
	}
	code, body = do("POST", "/api/startup", token, "", `{"autostart":true,"minimized":true,"background":true,"minimizeToTray":false}`)
	if json.Unmarshal([]byte(body), &s) != nil || code != 200 || !s.Autostart || !s.StartMinimized || !s.Background || s.MinimizeToTray {
		t.Fatalf("startup: %d %s", code, body)
	}
	if code, _ = do("POST", "/api/quit", token, "", `{}`); code != 200 {
		t.Fatalf("quit: %d", code)
	}
	<-quit
}

// rtNamesReset makes the next cycle ask the server for names and kinds again.
func (e *Engine) rtNamesReset(host string) {
	e.mu.Lock()
	if rt := e.servers[host]; rt != nil {
		rt.namesFor = ""
	}
	e.mu.Unlock()
	e.step()
}
