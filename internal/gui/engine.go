// Package gui is the desktop app: it keeps chosen devices of one or more
// servers attached and serves the settings window as a local web page.
package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"pulseusb/internal/attach"
	"pulseusb/internal/client"
	"pulseusb/internal/discover"
	"pulseusb/internal/proto"
	"pulseusb/internal/usbids"
)

// Version identifies this build; a newer build replaces a running older one.
const Version = "1.0.0"

// Backend is what the engine needs from the system; tests substitute it.
type Backend interface {
	List(host string) ([]*proto.Device, error)
	Attached() (map[string]int, error)
	Attach(host, busid string, once, streaming bool) error
	Detach(port int) error
	Discover() []discover.Server
	// Names returns what the server's devices call themselves, or nil if
	// the server cannot tell.
	Names(host string) map[string]string
	// Kinds returns what each device is ("Game controller", ...), or nil.
	Kinds(host string) map[string]string
	// Holders reports which devices are in use, and by which address; an
	// error means the server cannot tell (an older version).
	Holders(host string) (holders map[string]client.Holder, self string, err error)
	// Release takes a device from whichever computer holds it, for us.
	Release(host, busid string) error
}

// Autostart controls launching the app with the computer.
type Autostart interface {
	Supported() bool
	Enabled() bool
	// Set makes the app start when the user signs in.
	Set(enabled, minimized bool) error
	// BackgroundSupported reports whether the app can also run before
	// anyone signs in.
	BackgroundSupported() bool
	// SetBackground installs or removes that boot-time start; configPath is
	// the settings file the boot-time instance should follow.
	SetBackground(on bool, configPath string) error
	// BackgroundTarget is the program the boot-time start launches if set
	// up now. The start breaks when the program moves or is reinstalled
	// elsewhere, so the engine remembers the target it was set up for.
	BackgroundTarget() string
	// PrepareBackground refreshes what the boot-time instance reads at
	// start (no special rights needed).
	PrepareBackground(configPath string) error
}

// Setup installs what the app needs on this machine; nil where there is
// nothing to install.
type Setup interface {
	DriverInstalled() bool
	InstallDriver(report func(string)) error
	AppInstalled() bool
	InstallApp() error
}

// ServerConfig is one server the user connected to.
type ServerConfig struct {
	Name string `json:"name"` // discovered name, to find it again if its address changes
	Host string `json:"host"` // host:port
}

// DeviceConfig is what the user chose for one kind of device on a server.
type DeviceConfig struct {
	Hidden bool   `json:"hidden,omitempty"` // kept out of the lists and the tray menu
	Alias  string `json:"alias,omitempty"`  // the user's own name for the device
	Group  string `json:"group,omitempty"`  // devices in a group connect and disconnect together on request
	Auto   bool   `json:"auto"`             // connect at startup and whenever it appears
	Mode   string `json:"mode,omitempty"`   // "low-latency" or "zero-copy"; empty = by device type
}

// Config is stored as JSON.
type Config struct {
	// Shortcuts maps an action ("group:<name>" to switch a group on or
	// off, "all:off" to disconnect everything) to a keyboard shortcut.
	Shortcuts map[string]string `json:"shortcuts,omitempty"`
	// BackgroundExe is the program the boot-time start was set up to launch.
	BackgroundExe string `json:"backgroundExe,omitempty"`
	// Theme is "dark", "light", or empty to follow the system setting.
	Theme   string         `json:"theme,omitempty"`
	Servers []ServerConfig `json:"servers"`
	// Devices holds settings per server (by name, else address) and per
	// device (by vendor:product, so they survive a change of USB port).
	Devices        map[string]map[string]DeviceConfig `json:"devices"`
	StartMinimized bool                               `json:"startMinimized"`
	// KeepWindow turns off "minimize to tray": a minimized window then
	// stays on the taskbar. (Stored negated so the default is to hide.)
	KeepWindow  bool `json:"keepWindowOnTaskbar"`
	Background  bool `json:"background"`
	Initialized bool `json:"initialized"`

	// Settings from before version 0.6, migrated on load.
	OldHost string `json:"host,omitempty"`
	OldName string `json:"name,omitempty"`
}

// DeviceState is one device row of the window.
type DeviceState struct {
	// Elsewhere names the other computer using the device, when one is.
	Elsewhere string `json:"elsewhere,omitempty"`
	// Kept: the server machine keeps the device for itself for now.
	Kept bool `json:"kept,omitempty"`
	// Paused: auto-connect is on, but the user disconnected the device, which
	// holds until the app restarts or they connect it again.
	Paused bool   `json:"paused"`
	Hidden bool   `json:"hidden"`
	Alias  string `json:"alias"` // the user's own name, shown instead of Name when set
	Group  string `json:"group"`
	BusID  string `json:"busid"`
	ID     string `json:"id"`   // vendor:product
	Name   string `json:"name"` // what the device is, as best we know
	Kind   string `json:"kind"` // rough device type
	Auto   bool   `json:"auto"`
	Mode   string `json:"mode"`   // auto, low-latency or zero-copy, as chosen
	Actual string `json:"actual"` // the mode in effect
	Status string `json:"status"` // connected, connecting, disconnected, error
	Error  string `json:"error,omitempty"`
}

// GroupState is one group of devices the user defined.
type GroupState struct {
	Name      string `json:"name"`
	Total     int    `json:"total"`     // member devices currently offered by a server
	Connected int    `json:"connected"` // of those, how many are connected
	Elsewhere int    `json:"elsewhere"` // of those, how many another computer (or the server) has
}

// ServerState is one connected server with its devices.
type ServerState struct {
	Name    string        `json:"name"`
	Host    string        `json:"host"`
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Devices []DeviceState `json:"devices"`
}

// State is everything the window shows.
type State struct {
	Shortcuts        map[string]string `json:"shortcuts"`
	ShortcutErrors   map[string]string `json:"shortcutErrors"`
	HotkeysSupported bool              `json:"hotkeysSupported"`
	// Timings lists recent connects and slow background checks with how
	// long each part took, newest first, for troubleshooting.
	Timings []string `json:"timings"`
	// Rev increases whenever something the window shows may have changed;
	// the window waits on it instead of asking on a timer.
	Rev uint64 `json:"rev"`
	// BackgroundStale: starting at boot is switched on but was set up for
	// a program location that is no longer the current one.
	BackgroundStale     bool              `json:"backgroundStale"`
	Theme               string            `json:"theme"`      // "dark", "light" or "" for the system's
	Groups              []GroupState      `json:"groups"`     // groups with at least one device present
	GroupNames          []string          `json:"groupNames"` // every group the user has defined
	Version             string            `json:"version"`
	OS                  string            `json:"os"`
	Servers             []ServerState     `json:"servers"`
	Found               []discover.Server `json:"found"` // on the network, not connected
	Error               string            `json:"error,omitempty"`
	StartMinimized      bool              `json:"startMinimized"`
	MinimizeToTray      bool              `json:"minimizeToTray"`
	Autostart           bool              `json:"autostart"`
	AutostartSupported  bool              `json:"autostartSupported"`
	Background          bool              `json:"background"`
	BackgroundSupported bool              `json:"backgroundSupported"`
	Modes               bool              `json:"modes"` // receive modes exist on this system
	DriverMissing       bool              `json:"driverMissing"`
	DriverStatus        string            `json:"driverStatus,omitempty"`
	AppInstallable      bool              `json:"appInstallable"`
}

type serverRT struct {
	ok       bool
	err      string
	devices  []DeviceState
	names    map[string]string // bus id -> self-reported name
	namesFor string            // device set the names were fetched for
	namesAge int
	holders  map[string]client.Holder
	kinds    map[string]string // bus id -> what the server says the device is
}

// Engine keeps the wanted devices attached.
type Engine struct {
	Backend   Backend
	Autostart Autostart
	Setup     Setup
	Path      string // config file; empty keeps settings in memory only
	Interval  time.Duration
	Modes     bool // offer the receive-mode choice (Windows driver only)
	// ReadOnly makes the engine follow the settings file without ever
	// writing it (the boot-time instance, which runs as another account).
	ReadOnly bool
	// Kind is "gui" or "background", reported to newer instances.
	Kind string
	// OnQuit is called when another instance asks this one to stop.
	OnQuit func()

	mu           sync.Mutex
	cfg          Config
	servers      map[string]*serverRT // by host
	found        []discover.Server
	localErr     string
	wanted, held map[string]bool // this session's Connect / Disconnect clicks
	tick         int
	// lastHave is what was attached at the last full cycle, kept so that a
	// cycle triggered by a click need not ask the driver again first.
	lastHave map[string]int
	lastFull time.Time
	// needFull asks for the next cycle to check everything with the driver
	// and the servers (set after a click, so what it did is confirmed).
	needFull bool

	// Clicks are carried out at once, each on its own, never queued behind
	// the background cycle. inflight holds the devices being plugged in or
	// out that way; touched records when each last finished, so a cycle
	// that started earlier does not overwrite the result with older news.
	inflight map[string]bool
	touched  map[string]time.Time
	timings  []string

	// Hotkeys: whether this instance offers keyboard shortcuts (the tray
	// app on Windows), and why any of them could not be set up.
	Hotkeys      bool
	shortcutErrs map[string]string

	peerNames map[string]string // address -> computer name, looked up once

	// OnKeyboardGone, when set, is called after a keyboard or mouse has
	// left this computer (unplugged by us, or taken by another). A key
	// held at that moment would stay "pressed" here; this is the chance
	// to release it.
	OnKeyboardGone func()

	revMu        sync.Mutex
	rev          uint64
	revCh        chan struct{} // closed, and replaced, at every change
	driverStatus string
	installing   bool
	kick         chan struct{}
}

// DefaultPath is where the settings live.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "PulseUSB", "config.json")
}

// Load reads the stored settings, if any.
func (e *Engine) Load() {
	e.kick = make(chan struct{}, 1)
	e.servers = map[string]*serverRT{}
	e.wanted, e.held = map[string]bool{}, map[string]bool{}
	e.inflight, e.touched = map[string]bool{}, map[string]time.Time{}
	e.peerNames = map[string]string{}
	if data, err := os.ReadFile(e.Path); err == nil {
		json.Unmarshal(data, &e.cfg)
	}
	if e.cfg.OldHost != "" && len(e.cfg.Servers) == 0 {
		e.cfg.Servers = []ServerConfig{{Name: e.cfg.OldName, Host: client.Addr(e.cfg.OldHost)}}
		e.cfg.Initialized = true
	}
	e.cfg.OldHost, e.cfg.OldName = "", ""
	if e.cfg.Devices == nil {
		e.cfg.Devices = map[string]map[string]DeviceConfig{}
	}
}

// save must be called with e.mu held.
func (e *Engine) save() {
	if e.Path == "" || e.ReadOnly {
		return
	}
	data, _ := json.MarshalIndent(e.cfg, "", "  ")
	os.MkdirAll(filepath.Dir(e.Path), 0o755)
	os.WriteFile(e.Path, data, 0o644)
}

// Config returns a copy of the current settings.
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.cfg
	c.Servers = append([]ServerConfig{}, c.Servers...)
	return c
}

func serverKey(s ServerConfig) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Host
}

// changed tells waiting windows that there is something new to show.
func (e *Engine) changed() {
	e.revMu.Lock()
	e.rev++
	if e.revCh != nil {
		close(e.revCh)
	}
	e.revCh = make(chan struct{})
	e.revMu.Unlock()
}

// Wait returns as soon as the state has moved past revision since, or after
// timeout if nothing changed.
func (e *Engine) Wait(since uint64, timeout time.Duration) {
	e.revMu.Lock()
	if e.revCh == nil {
		e.revCh = make(chan struct{})
	}
	ch, now := e.revCh, e.rev
	e.revMu.Unlock()
	if now != since {
		return
	}
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

// State returns what the window should show right now.
func (e *Engine) State() State {
	e.revMu.Lock()
	rev := e.rev
	e.revMu.Unlock()
	e.mu.Lock()
	s := State{
		Timings:          append([]string{}, e.timings...),
		Shortcuts:        map[string]string{},
		ShortcutErrors:   map[string]string{},
		HotkeysSupported: e.Hotkeys,
		Rev:              rev,
		Version:          Version,
		OS:               runtime.GOOS,
		Servers:          []ServerState{},
		Found:            []discover.Server{},
		Error:            e.localErr,
		StartMinimized:   e.cfg.StartMinimized,
		Theme:            e.cfg.Theme,
		MinimizeToTray:   !e.cfg.KeepWindow,
		Background:       e.cfg.Background,
		Modes:            e.Modes,
		DriverStatus:     e.driverStatus,
	}
	for k, v := range e.cfg.Shortcuts {
		s.Shortcuts[k] = v
	}
	for k, v := range e.shortcutErrs {
		s.ShortcutErrors[k] = v
	}
	connected := map[string]bool{}
	for _, sc := range e.cfg.Servers {
		connected[sc.Host] = true
		st := ServerState{Name: sc.Name, Host: sc.Host, Devices: []DeviceState{}}
		if rt := e.servers[sc.Host]; rt != nil {
			st.OK, st.Error = rt.ok, rt.err
			st.Devices = append(st.Devices, rt.devices...)
		}
		s.Servers = append(s.Servers, st)
	}
	names := map[string]bool{}
	for _, devs := range e.cfg.Devices {
		for _, dc := range devs {
			if dc.Group != "" {
				names[dc.Group] = true
			}
		}
	}
	s.GroupNames = make([]string, 0, len(names))
	for n := range names {
		s.GroupNames = append(s.GroupNames, n)
	}
	sort.Strings(s.GroupNames)
	s.Groups = []GroupState{}
	for _, n := range s.GroupNames {
		g := GroupState{Name: n}
		for _, sv := range s.Servers {
			for _, d := range sv.Devices {
				if d.Group == n && !d.Hidden {
					g.Total++
					if d.Status == "connected" {
						g.Connected++
					}
					if d.Status == "elsewhere" || d.Status == "kept" {
						g.Elsewhere++
					}
				}
			}
		}
		s.Groups = append(s.Groups, g)
	}
	for _, f := range e.found {
		if !connected[f.Addr] {
			s.Found = append(s.Found, f)
		}
	}
	e.mu.Unlock()
	if e.Autostart != nil {
		s.AutostartSupported = e.Autostart.Supported()
		s.Autostart = s.AutostartSupported && e.Autostart.Enabled()
		s.BackgroundSupported = e.Autostart.BackgroundSupported()
		s.BackgroundStale = e.BackgroundStale()
	}
	if e.Setup != nil {
		s.DriverMissing = !e.Setup.DriverInstalled()
		s.AppInstallable = !e.Setup.AppInstalled()
		if !s.DriverMissing {
			s.DriverStatus = ""
		} else {
			s.Error = "" // the driver notice says it better
		}
	}
	return s
}

func (e *Engine) wake() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// AddServer connects to a server. name is its discovered name, if known.
func (e *Engine) AddServer(host, name string) {
	if host = strings.TrimSpace(host); host == "" {
		return
	}
	host = client.Addr(host)
	e.mu.Lock()
	found := false
	for i := range e.cfg.Servers {
		if e.cfg.Servers[i].Host == host {
			found = true
			if name != "" {
				e.cfg.Servers[i].Name = name
			}
		}
	}
	if !found {
		e.cfg.Servers = append(e.cfg.Servers, ServerConfig{Name: name, Host: host})
	}
	e.cfg.Initialized = true
	e.save()
	e.mu.Unlock()
	e.wake()
}

// RemoveServer disconnects from a server: its devices are unplugged and it
// leaves the list. Its device settings are kept for next time.
func (e *Engine) RemoveServer(host string) {
	e.mu.Lock()
	var keep []ServerConfig
	for _, sc := range e.cfg.Servers {
		if sc.Host != host {
			keep = append(keep, sc)
		}
	}
	e.cfg.Servers = keep
	delete(e.servers, host)
	e.save()
	e.mu.Unlock()
	e.mu.Lock()
	e.lastHave = nil // ask the driver afresh next time
	e.mu.Unlock()
	if have, err := e.Backend.Attached(); err == nil {
		prefix := attach.Key(host, "")
		for key, port := range have {
			if strings.HasPrefix(key, prefix) {
				e.Backend.Detach(port)
			}
		}
	}
	e.wake()
}

// locate finds a server and the row of one of its devices. Call with e.mu held.
func (e *Engine) locate(host, busid string) (ServerConfig, *DeviceState) {
	for _, sc := range e.cfg.Servers {
		if sc.Host != host {
			continue
		}
		if rt := e.servers[host]; rt != nil {
			for i := range rt.devices {
				if rt.devices[i].BusID == busid {
					return sc, &rt.devices[i]
				}
			}
		}
	}
	return ServerConfig{}, nil
}

func (e *Engine) detach(host, busid string) {
	e.mu.Lock()
	delete(e.lastHave, attach.Key(host, busid))
	e.mu.Unlock()
	if have, err := e.Backend.Attached(); err == nil {
		if port, ok := have[attach.Key(host, busid)]; ok {
			e.Backend.Detach(port)
		}
	}
}

// SetConnected handles a Connect or Disconnect click on a device. The
// choice holds until the app restarts; auto-connect decides after that.
func (e *Engine) SetConnected(host, busid string, on bool) {
	e.mu.Lock()
	sc, row := e.locate(host, busid)
	if row == nil {
		e.mu.Unlock()
		return
	}
	k := serverKey(sc) + "|" + row.ID
	e.wanted[k], e.held[k] = on, !on
	row.Error = ""
	if on && row.Status != "connected" {
		row.Status = "connecting"
	} else if !on {
		row.Status = "disconnected"
	}
	row.Paused = !on && row.Auto
	name, streaming, already := deviceTitle(*row), row.Actual == "zero-copy", row.Status == "connected"
	e.mu.Unlock()
	e.changed()
	if on && already {
		return
	}
	e.act(host, busid, name, on, false, streaming, time.Now())
}

// TakeOver connects a device that another computer is using: that computer
// loses it, and it is plugged in here.
func (e *Engine) TakeOver(host, busid string) {
	e.mu.Lock()
	sc, row := e.locate(host, busid)
	if row == nil {
		e.mu.Unlock()
		return
	}
	k := serverKey(sc) + "|" + row.ID
	e.wanted[k], e.held[k] = true, false
	row.Error, row.Status, row.Paused = "", "connecting", false
	name, streaming := deviceTitle(*row), row.Actual == "zero-copy"
	e.mu.Unlock()
	e.changed()
	e.act(host, busid, name, true, true, streaming, time.Now())
}

// deviceTitle is how a device is named in the timing list.
func deviceTitle(d DeviceState) string {
	for _, s := range []string{d.Alias, d.Name, d.Kind + " " + d.ID} {
		if s != "" {
			return s
		}
	}
	return d.BusID
}

// Note adds a line to the timing list, for use outside the engine.
func (e *Engine) Note(format string, args ...any) { e.note(format, args...) }

// note adds a line to the timing list (newest first, the last 12 kept).
func (e *Engine) note(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	e.mu.Lock()
	e.timings = append([]string{line}, e.timings...)
	if len(e.timings) > 12 {
		e.timings = e.timings[:12]
	}
	e.mu.Unlock()
	e.changed()
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// act plugs a device in or out right away, on its own, so that a click is
// never queued behind the background cycle (which can be slow, for
// example while a server in the list does not answer).
func (e *Engine) act(host, busid, name string, on, takeover, streaming bool, clicked time.Time) {
	key := attach.Key(host, busid)
	e.mu.Lock()
	if e.inflight[key] {
		e.mu.Unlock()
		return // already being done; the background cycle catches up after
	}
	e.inflight[key] = true
	e.mu.Unlock()
	go func() {
		var err error
		var driver time.Duration
		if on {
			if takeover {
				err = e.Backend.Release(host, busid)
			}
			if err == nil {
				start := time.Now()
				err = e.Backend.Attach(host, busid, true, streaming)
				driver = time.Since(start)
			}
		} else {
			start := time.Now()
			e.detach(host, busid)
			driver = time.Since(start)
			e.keyboardGone(host, []string{busid})
		}
		e.mu.Lock()
		delete(e.inflight, key)
		e.touched[key] = time.Now()
		if rt := e.servers[host]; rt != nil {
			for i := range rt.devices {
				if r := &rt.devices[i]; r.BusID == busid {
					switch {
					case on && err != nil:
						r.Status, r.Error = "error", err.Error()
					case on:
						r.Status, r.Error, r.Elsewhere = "connected", "", ""
					default:
						r.Status = "disconnected"
					}
				}
			}
		}
		if on && err == nil && e.lastHave != nil {
			e.lastHave[key] = -1 // attached; port not known yet
		}
		e.needFull = true
		e.mu.Unlock()
		e.wake() // confirm what really happened, straight away
		verb := "Connect"
		if takeover {
			verb = "Take over"
		}
		if !on {
			verb = "Disconnect"
		}
		if err != nil {
			e.note("%s %s failed after %d ms: %v", verb, name, ms(time.Since(clicked)), err)
		} else {
			e.note("%s %s: %d ms (driver %d ms)", verb, name, ms(time.Since(clicked)), ms(driver))
		}
	}()
}

func (e *Engine) editDevice(host, busid string, edit func(*DeviceConfig, string)) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	sc, row := e.locate(host, busid)
	if row == nil {
		return false
	}
	skey := serverKey(sc)
	if e.cfg.Devices[skey] == nil {
		e.cfg.Devices[skey] = map[string]DeviceConfig{}
	}
	dc := e.cfg.Devices[skey][row.ID]
	edit(&dc, skey+"|"+row.ID)
	e.cfg.Devices[skey][row.ID] = dc
	row.Auto = dc.Auto
	row.Alias, row.Group = dc.Alias, dc.Group
	row.Hidden = dc.Hidden
	row.Paused = dc.Auto && e.held[skey+"|"+row.ID]
	e.save()
	return true
}

// SetAuto switches auto-connect for a device. Switching it on also connects
// the device now; switching it off leaves the device as it is.
func (e *Engine) SetAuto(host, busid string, auto bool) {
	e.editDevice(host, busid, func(dc *DeviceConfig, k string) {
		dc.Auto = auto
		if auto {
			e.held[k] = false
		}
	})
	e.wake()
}

// clip trims what the user typed and bounds its length.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// SetAlias gives a device the user's own name; an empty alias restores the
// device's real name.
func (e *Engine) SetAlias(host, busid, alias string) {
	e.editDevice(host, busid, func(dc *DeviceConfig, _ string) { dc.Alias = clip(alias, 60) })
}

// SetGroup puts a device in a group, or with an empty name takes it out.
func (e *Engine) SetGroup(host, busid, group string) {
	e.editDevice(host, busid, func(dc *DeviceConfig, _ string) { dc.Group = clip(group, 30) })
}

// SetHidden hides a device the user has no use for, or brings it back. A
// hidden device is not auto-connected.
func (e *Engine) SetHidden(host, busid string, hidden bool) {
	e.editDevice(host, busid, func(dc *DeviceConfig, _ string) {
		dc.Hidden = hidden
		if hidden {
			dc.Auto = false
		}
	})
}

// SetTheme chooses the window's colours: "dark", "light", or anything else
// to follow the system setting.
func (e *Engine) SetTheme(theme string) {
	if theme != "dark" && theme != "light" {
		theme = ""
	}
	e.mu.Lock()
	e.cfg.Theme = theme
	e.save()
	e.mu.Unlock()
}

// RenameGroup gives a group a new name. Renaming onto an existing group
// merges the two.
func (e *Engine) RenameGroup(old, name string) {
	name = clip(name, 30)
	if old == "" || name == "" || name == old {
		return
	}
	e.mu.Lock()
	for skey, devs := range e.cfg.Devices {
		for id, dc := range devs {
			if dc.Group == old {
				dc.Group = name
				e.cfg.Devices[skey][id] = dc
			}
		}
	}
	for _, rt := range e.servers {
		for i := range rt.devices {
			if rt.devices[i].Group == old {
				rt.devices[i].Group = name
			}
		}
	}
	if c, ok := e.cfg.Shortcuts["group:"+old]; ok {
		delete(e.cfg.Shortcuts, "group:"+old)
		e.cfg.Shortcuts["group:"+name] = c
	}
	e.save()
	e.mu.Unlock()
}

// SetShortcut assigns a keyboard shortcut to an action, or with an empty
// shortcut removes it. A shortcut belongs to one action only.
func (e *Engine) SetShortcut(target, combo string) error {
	canon := ""
	if strings.TrimSpace(combo) != "" {
		var ok bool
		if _, _, canon, ok = ParseShortcut(combo); !ok {
			return fmt.Errorf("%q is not a shortcut this app can use: it needs Ctrl, Alt or Win plus a letter, digit or F-key", combo)
		}
	}
	e.mu.Lock()
	if e.cfg.Shortcuts == nil {
		e.cfg.Shortcuts = map[string]string{}
	}
	for t, c := range e.cfg.Shortcuts {
		if c == canon {
			delete(e.cfg.Shortcuts, t)
		}
	}
	if canon == "" {
		delete(e.cfg.Shortcuts, target)
	} else {
		e.cfg.Shortcuts[target] = canon
	}
	e.save()
	e.mu.Unlock()
	e.changed()
	return nil
}

// Shortcuts returns the shortcuts to set up.
func (e *Engine) Shortcuts() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]string{}
	for k, v := range e.cfg.Shortcuts {
		out[k] = v
	}
	return out
}

// SetShortcutErrors records shortcuts that could not be set up, and why.
func (e *Engine) SetShortcutErrors(errs map[string]string) {
	e.mu.Lock()
	e.shortcutErrs = errs
	e.mu.Unlock()
	e.changed()
}

// Trigger carries out the action a shortcut was pressed for. A group's
// shortcut switches the group: off when all of it is connected here,
// otherwise on, taking devices from another computer if need be.
//
// The decision is made from what the driver says is attached right now, not
// from what the window last showed, and a press is ignored while the
// previous one is still being carried out.
func (e *Engine) Trigger(target string) {
	type dev struct{ host, busid string }
	var members []dev
	busy := false
	e.mu.Lock()
	for _, sc := range e.cfg.Servers {
		rt := e.servers[sc.Host]
		if rt == nil {
			continue
		}
		for _, d := range rt.devices {
			in := target == "all:off" || (strings.HasPrefix(target, "group:") && d.Group == strings.TrimPrefix(target, "group:"))
			if !in || d.Hidden {
				continue
			}
			members = append(members, dev{sc.Host, d.BusID})
			busy = busy || e.inflight[attach.Key(sc.Host, d.BusID)]
		}
	}
	e.mu.Unlock()
	if busy {
		e.note("Shortcut pressed while the last one was still being carried out; ignored")
		return
	}
	have, err := e.Backend.Attached()
	if err != nil {
		e.note("Shortcut: could not ask the driver what is connected: %v", err)
		return
	}
	attached := 0
	for _, m := range members {
		if _, ok := have[attach.Key(m.host, m.busid)]; ok {
			attached++
		}
	}
	// Bring the remembered state in line before acting on it.
	e.mu.Lock()
	e.lastHave = have
	for _, sc := range e.cfg.Servers {
		if rt := e.servers[sc.Host]; rt != nil {
			for i := range rt.devices {
				d := &rt.devices[i]
				_, on := have[attach.Key(sc.Host, d.BusID)]
				if on {
					d.Status = "connected"
				} else if d.Status == "connected" {
					d.Status = "disconnected"
				}
			}
		}
	}
	e.mu.Unlock()
	switch {
	case target == "all:off":
		for _, m := range members {
			if _, ok := have[attach.Key(m.host, m.busid)]; ok {
				e.SetConnected(m.host, m.busid, false)
			}
		}
		e.note("Shortcut: disconnect everything (%d connected)", attached)
	case len(members) == 0:
	case attached == len(members):
		name := strings.TrimPrefix(target, "group:")
		e.SetGroupConnected(name, false)
		e.note("Shortcut: %s off", name)
	default:
		name := strings.TrimPrefix(target, "group:")
		e.TakeOverGroup(name)
		e.note("Shortcut: %s on (%d of %d were connected)", name, attached, len(members))
	}
}

// peerName turns a computer's address into its name when the network
// knows one; the lookup happens once, in the background.
func (e *Engine) peerName(addr string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n, ok := e.peerNames[addr]; ok {
		return n
	}
	e.peerNames[addr] = addr
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		names, err := net.DefaultResolver.LookupAddr(ctx, addr)
		if err != nil || len(names) == 0 {
			return
		}
		n := strings.TrimSuffix(names[0], ".")
		if i := strings.IndexByte(n, '.'); i > 0 {
			n = n[:i] // "gaming-pc.lan" -> "gaming-pc"
		}
		e.mu.Lock()
		e.peerNames[addr] = n
		e.mu.Unlock()
		e.changed()
	}()
	return addr
}

// DeleteGroup dissolves a group: its devices stay as they are, ungrouped.
func (e *Engine) DeleteGroup(group string) {
	if group == "" {
		return
	}
	e.mu.Lock()
	for skey, devs := range e.cfg.Devices {
		for id, dc := range devs {
			if dc.Group == group {
				dc.Group = ""
				e.cfg.Devices[skey][id] = dc
			}
		}
	}
	for _, rt := range e.servers {
		for i := range rt.devices {
			if rt.devices[i].Group == group {
				rt.devices[i].Group = ""
			}
		}
	}
	delete(e.cfg.Shortcuts, "group:"+group)
	e.save()
	e.mu.Unlock()
}

// SetGroupConnected connects or disconnects every present device of a
// group, exactly as if each had been clicked; they are handled side by side.
func (e *Engine) SetGroupConnected(group string, on bool) { e.setGroup(group, on, false) }

// TakeOverGroup connects every device of a group, taking any that another
// computer is using.
func (e *Engine) TakeOverGroup(group string) { e.setGroup(group, true, true) }

func (e *Engine) setGroup(group string, on, takeover bool) {
	type member struct {
		host, busid string
		elsewhere   bool
	}
	var members []member
	e.mu.Lock()
	for _, sc := range e.cfg.Servers {
		if rt := e.servers[sc.Host]; rt != nil {
			for _, d := range rt.devices {
				if d.Group == group && group != "" && !d.Hidden {
					members = append(members, member{sc.Host, d.BusID, d.Status == "elsewhere" || d.Status == "kept"})
				}
			}
		}
	}
	e.mu.Unlock()
	for _, m := range members {
		if takeover && m.elsewhere {
			e.TakeOver(m.host, m.busid)
		} else {
			e.SetConnected(m.host, m.busid, on)
		}
	}
}

// SetMode chooses a device's receive mode ("auto", "low-latency" or
// "zero-copy"). A connected device is unplugged and comes back in the new mode.
func (e *Engine) SetMode(host, busid, mode string) {
	reconnect := false
	ok := e.editDevice(host, busid, func(dc *DeviceConfig, k string) {
		dc.Mode = ""
		if mode == "low-latency" || mode == "zero-copy" {
			dc.Mode = mode
		}
		_, row := e.locate(host, busid)
		if row.Status == "connected" {
			reconnect = true
			e.wanted[k], e.held[k] = true, false
		}
	})
	if ok && reconnect {
		e.detach(host, busid)
	}
	e.wake()
}

// InstallDriver starts the driver installation in the background; progress
// shows up in State.
func (e *Engine) InstallDriver() {
	e.mu.Lock()
	if e.Setup == nil || e.installing {
		e.mu.Unlock()
		return
	}
	e.installing = true
	e.driverStatus = "Starting…"
	e.mu.Unlock()
	go func() {
		report := func(msg string) {
			e.mu.Lock()
			e.driverStatus = msg
			e.mu.Unlock()
			e.changed()
		}
		if err := e.Setup.InstallDriver(report); err != nil {
			report("Could not install the driver: " + err.Error())
		}
		e.mu.Lock()
		e.installing = false
		e.mu.Unlock()
	}()
}

// InstallApp copies the app to its permanent location.
func (e *Engine) InstallApp() error {
	if e.Setup == nil {
		return nil
	}
	return e.Setup.InstallApp()
}

// Startup is the set of choices about how the app starts and where its
// window goes.
type Startup struct {
	Autostart      bool // launch when the user signs in
	Minimized      bool // start without opening the window
	MinimizeToTray bool // a minimized window leaves the taskbar
	Background     bool // also run before sign-in
}

// SetStartup applies those choices. Any start with the computer runs from
// the installed copy, so that is put in place first.
func (e *Engine) SetStartup(st Startup) error {
	autostart, minimized, background := st.Autostart, st.Minimized, st.Background
	e.mu.Lock()
	was := e.cfg.Background
	e.cfg.StartMinimized = minimized
	e.cfg.KeepWindow = !st.MinimizeToTray
	e.save()
	e.mu.Unlock()
	if e.Autostart == nil || !e.Autostart.Supported() {
		return nil
	}
	if (autostart || background) && e.Setup != nil && !e.Setup.AppInstalled() {
		if err := e.Setup.InstallApp(); err != nil {
			return err
		}
	}
	if err := e.Autostart.Set(autostart, minimized); err != nil {
		return err
	}
	if background != was && e.Autostart.BackgroundSupported() {
		if err := e.Autostart.SetBackground(background, e.Path); err != nil {
			return err
		}
		e.mu.Lock()
		e.cfg.Background = background
		e.cfg.BackgroundExe = ""
		if background {
			e.cfg.BackgroundExe = e.Autostart.BackgroundTarget()
		}
		e.save()
		e.mu.Unlock()
	}
	return nil
}

// BackgroundStale reports whether starting at boot is switched on but no
// longer points at the installed program (it was moved or reinstalled, or
// was set up by a version that did not record where it pointed).
func (e *Engine) BackgroundStale() bool {
	if e.Autostart == nil || !e.Autostart.BackgroundSupported() {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Background && e.cfg.BackgroundExe != e.Autostart.BackgroundTarget()
}

// RepairBackground sets the boot-time start up again for the program's
// current location.
func (e *Engine) RepairBackground() error {
	if e.Autostart == nil || !e.Autostart.BackgroundSupported() {
		return nil
	}
	if e.Setup != nil && !e.Setup.AppInstalled() {
		if err := e.Setup.InstallApp(); err != nil {
			return err
		}
	}
	if err := e.Autostart.SetBackground(true, e.Path); err != nil {
		return err
	}
	e.mu.Lock()
	e.cfg.Background = true
	e.cfg.BackgroundExe = e.Autostart.BackgroundTarget()
	e.save()
	e.mu.Unlock()
	return nil
}

// PrepareBackground refreshes the boot-time instance's pointer to the
// settings, which a reinstall can remove. Call once at start.
func (e *Engine) PrepareBackground() {
	if e.Autostart == nil || !e.Autostart.BackgroundSupported() {
		return
	}
	e.mu.Lock()
	on := e.cfg.Background
	e.mu.Unlock()
	if on {
		e.Autostart.PrepareBackground(e.Path)
	}
}

// Quit stops this instance on behalf of a newer one taking over.
func (e *Engine) Quit() {
	if e.OnQuit != nil {
		go e.OnQuit()
	}
}

// Run attaches and re-attaches devices until stop is closed.
func (e *Engine) Run(stop <-chan struct{}) {
	interval := e.Interval
	if interval == 0 {
		interval = 2 * time.Second
	}
	fast := false
	for {
		// A cycle started by a click is a fast one, unless it is time for a
		// full one anyway.
		e.cycle(fast && time.Since(e.lastFull) < interval)
		select {
		case <-stop:
			return
		case <-e.kick:
			fast = true
		case <-time.After(interval):
			fast = false
		}
	}
}

// step runs one full cycle.
func (e *Engine) step() { e.cycle(false) }

// kindOf says what a device is: as the server read it from the device's own
// description when it can; otherwise guessed from the device's name, and
// last from the kinds of its USB interfaces. (A game controller with a
// headphone jack, like a DualSense, has an audio interface too, so the
// interfaces alone would call it audio.)
func kindOf(d *proto.Device, fromServer, name string) string {
	if fromServer != "" {
		return fromServer
	}
	if name == "" {
		name = usbids.Describe("", d.VendorID, d.ProductID)
	}
	n := strings.ToLower(name)
	for _, w := range []string{"controller", "gamepad", "joystick", "joy-con", "wheel", "pedal", "shifter",
		"handbrake", "button box", "throttle", "hotas", "rudder", "flight stick", "dualsense", "dualshock"} {
		if strings.Contains(n, w) {
			return "Game controller"
		}
	}
	classes := map[uint8]bool{d.Class: true}
	for _, i := range d.Interfaces {
		classes[i.Class] = true
	}
	switch {
	case classes[0x01]:
		return "Audio"
	case classes[0x0e]:
		return "Camera"
	case classes[0x08]:
		return "Storage"
	case classes[0x03]:
		return "Input device"
	case classes[0x02], classes[0x0a]:
		return "Serial / network"
	case classes[0x07]:
		return "Printer"
	}
	return "USB device"
}

// search looks for servers on the network. On the very first run, a lone
// server is connected without being asked. A known server that turns up at
// a new address (e.g. after its DHCP lease changed) is followed there.
func (e *Engine) search() {
	found := e.Backend.Discover()
	e.mu.Lock()
	e.found = found
	first := !e.cfg.Initialized && len(e.cfg.Servers) == 0 && len(found) == 1
	changed := false
	for i := range e.cfg.Servers {
		sc := &e.cfg.Servers[i]
		rt := e.servers[sc.Host]
		if sc.Name == "" || (rt != nil && rt.ok) {
			continue
		}
		var match []discover.Server
		for _, f := range found {
			if f.Name == sc.Name {
				match = append(match, f)
			}
		}
		if len(match) == 1 && match[0].Addr != sc.Host {
			delete(e.servers, sc.Host)
			sc.Host, changed = match[0].Addr, true
		}
	}
	if changed {
		e.save()
	}
	e.mu.Unlock()
	if first {
		e.AddServer(found[0].Addr, found[0].Name)
	}
}

// cycle brings what is attached in line with what is wanted. A full cycle
// first looks for servers (when needed) and asks the driver what is
// attached. A fast cycle, run in answer to a click, skips both and works
// from what the last full cycle found, so the click is acted on at once;
// the next full cycle, at most a couple of seconds later, re-checks.
func (e *Engine) cycle(fast bool) {
	if e.ReadOnly { // follow the settings file as the user's instance writes it
		if data, err := os.ReadFile(e.Path); err == nil {
			var c Config
			if json.Unmarshal(data, &c) == nil && c.Devices != nil {
				e.mu.Lock()
				e.cfg = c
				e.mu.Unlock()
			}
		}
	}
	var have map[string]int
	var herr error
	began := time.Now()
	var searchTook, checkTook time.Duration
	e.mu.Lock()
	if e.needFull {
		fast, e.needFull = false, false
	}
	if fast && e.lastHave != nil {
		have = make(map[string]int, len(e.lastHave))
		for k, v := range e.lastHave {
			have[k] = v
		}
	}
	e.mu.Unlock()
	if have == nil {
		e.mu.Lock()
		e.tick++
		lost := len(e.cfg.Servers) == 0 || e.tick%5 == 1
		for _, sc := range e.cfg.Servers {
			if rt := e.servers[sc.Host]; rt == nil || !rt.ok {
				lost = true
			}
		}
		e.mu.Unlock()
		if lost {
			t := time.Now()
			e.search()
			searchTook = time.Since(t)
		}
		t := time.Now()
		have, herr = e.Backend.Attached()
		checkTook = time.Since(t)
		e.mu.Lock()
		// Devices that were here at the last check and are gone now, without
		// a click here: taken by another computer, or the link dropped.
		gone := map[string][]string{}
		if herr == nil {
			for _, sc := range e.cfg.Servers {
				if rt := e.servers[sc.Host]; rt != nil {
					for _, d := range rt.devices {
						k := attach.Key(sc.Host, d.BusID)
						_, was := e.lastHave[k]
						_, is := have[k]
						if was && !is && !e.inflight[k] {
							gone[sc.Host] = append(gone[sc.Host], d.BusID)
						}
					}
				}
			}
		}
		e.lastFull = time.Now()
		e.localErr, e.lastHave = "", nil
		if herr != nil {
			e.localErr = herr.Error()
		} else {
			e.lastHave = make(map[string]int, len(have))
			for k, v := range have {
				e.lastHave[k] = v
			}
		}
		e.mu.Unlock()
		for host, busids := range gone {
			e.keyboardGone(host, busids)
		}
	}
	e.mu.Lock()
	servers := append([]ServerConfig{}, e.cfg.Servers...)
	e.mu.Unlock()
	// Each server is handled on its own, so one that does not answer never
	// holds up the others.
	var wg sync.WaitGroup
	var tmu sync.Mutex
	took := map[string]time.Duration{}
	for _, sc := range servers {
		wg.Add(1)
		go func(sc ServerConfig) {
			defer wg.Done()
			t := time.Now()
			e.stepServer(sc, have, herr)
			tmu.Lock()
			took[serverKey(sc)] = time.Since(t)
			tmu.Unlock()
			e.changed() // show this server's result as soon as it is in
		}(sc)
	}
	wg.Wait()
	if total := time.Since(began); total > time.Second {
		parts := []string{}
		if searchTook > 0 {
			parts = append(parts, fmt.Sprintf("network search %d ms", ms(searchTook)))
		}
		if checkTook > 0 {
			parts = append(parts, fmt.Sprintf("driver check %d ms", ms(checkTook)))
		}
		names := make([]string, 0, len(took))
		for n := range took {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s %d ms", n, ms(took[n])))
		}
		e.note("Background check took %d ms: %s", ms(total), strings.Join(parts, ", "))
	}
	e.changed()
}

// keyboardGone calls OnKeyboardGone if any of the devices that just left
// this computer is a keyboard or mouse (or an input device of unknown kind).
func (e *Engine) keyboardGone(host string, busids []string) {
	if e.OnKeyboardGone == nil {
		return
	}
	e.mu.Lock()
	hit := false
	if rt := e.servers[host]; rt != nil {
		for _, d := range rt.devices {
			for _, b := range busids {
				if d.BusID == b && (strings.Contains(d.Kind, "Keyboard") || d.Kind == "Mouse" || d.Kind == "Input device") {
					hit = true
				}
			}
		}
	}
	e.mu.Unlock()
	if hit {
		go e.OnKeyboardGone()
	}
}

// handledSince reports whether a click is plugging the device in or out
// right now, or finished doing so after the given time.
func (e *Engine) handledSince(key string, t time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inflight[key] || e.touched[key].After(t)
}

// settle waits until the clicks being carried out have finished.
func (e *Engine) settle() {
	for {
		e.mu.Lock()
		n := len(e.inflight)
		e.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// want reports whether a device should be attached now. Call with e.mu held.
func (e *Engine) want(sc ServerConfig, id string) bool {
	k := serverKey(sc) + "|" + id
	return e.wanted[k] || (e.cfg.Devices[serverKey(sc)][id].Auto && !e.held[k])
}

func (e *Engine) stepServer(sc ServerConfig, have map[string]int, herr error) {
	began := time.Now()
	e.mu.Lock()
	rt := e.servers[sc.Host]
	if rt == nil {
		rt = &serverRT{}
		e.servers[sc.Host] = rt
	}
	e.mu.Unlock()

	devs, err := e.Backend.List(sc.Host)
	if err != nil {
		// Unreachable: keep showing what we knew; the driver may still
		// hold some devices.
		e.mu.Lock()
		defer e.mu.Unlock()
		rt.ok, rt.err = false, fmt.Sprintf("Cannot reach this server: %v", err)
		for i := range rt.devices {
			d := &rt.devices[i]
			d.Error = ""
			if _, ok := have[attach.Key(sc.Host, d.BusID)]; ok {
				d.Status = "connected"
			} else if e.want(sc, d.ID) {
				d.Status = "connecting"
			} else {
				d.Status = "disconnected"
			}
		}
		return
	}

	// Ask for self-reported names when the device set changes, and now and
	// then in case the server was upgraded to one that knows them.
	set := sc.Host
	for _, d := range devs {
		set += fmt.Sprintf(" %s=%04x:%04x", d.BusID, d.VendorID, d.ProductID)
	}
	rt.namesAge++
	if set != rt.namesFor || (len(rt.names) == 0 && rt.namesAge > 150) {
		rt.names, rt.namesFor, rt.namesAge = e.Backend.Names(sc.Host), set, 0
		rt.kinds = e.Backend.Kinds(sc.Host)
	}
	holders, self, _ := e.Backend.Holders(sc.Host) // nil from servers that cannot tell

	rows := make([]DeviceState, 0, len(devs))
	for _, d := range devs {
		row := DeviceState{
			BusID: d.BusID,
			ID:    fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID),
			Name:  usbids.Describe(rt.names[d.BusID], d.VendorID, d.ProductID),
			Kind:  kindOf(d, rt.kinds[d.BusID], rt.names[d.BusID]),
			Mode:  "auto",
		}
		e.mu.Lock()
		dc := e.cfg.Devices[serverKey(sc)][row.ID]
		want := e.want(sc, row.ID)
		row.Paused = dc.Auto && e.held[serverKey(sc)+"|"+row.ID]
		e.mu.Unlock()
		row.Auto = dc.Auto
		row.Alias, row.Group = dc.Alias, dc.Group
		row.Hidden = dc.Hidden
		// Sound cards and microphones stream continuously, like webcams,
		// and the driver's zero-copy mode is the one meant for that (low
		// latency mode was found to put a whine on a microphone). Game
		// controllers keep low latency even when they have audio inside,
		// like a DualSense: their buttons and sticks matter more.
		streaming := attach.Streaming(d) || row.Kind == "Audio"
		if dc.Mode != "" {
			row.Mode, streaming = dc.Mode, dc.Mode == "zero-copy"
		}
		row.Actual = "low-latency"
		if streaming {
			row.Actual = "zero-copy"
		}
		_, attached := have[attach.Key(sc.Host, d.BusID)]
		switch {
		case herr != nil:
			row.Status = "error"
		case attached:
			row.Status = "connected"
		case self != "" && holders[d.BusID].Peer == self:
			// The server says this computer holds it, though the driver's
			// list does not show it yet: it is ours, not another's.
			row.Status = "connected"
		case holders["*"].Kept || holders[d.BusID].Kept:
			row.Status, row.Kept = "kept", true
		case holders[d.BusID].Peer != "":
			row.Status, row.Elsewhere = "elsewhere", e.peerName(holders[d.BusID].Peer)
			// Taken over by another computer: a Connect clicked here
			// earlier no longer applies (auto-connect still does).
			e.mu.Lock()
			delete(e.wanted, serverKey(sc)+"|"+row.ID)
			e.mu.Unlock()
		case e.handledSince(attach.Key(sc.Host, d.BusID), began):
			// A click is handling it, or did while this cycle ran: leave
			// it, and its row, to that.
			row.Status = "connecting"
		case want:
			if err := e.Backend.Attach(sc.Host, d.BusID, true, streaming); err != nil {
				row.Status, row.Error = "error", err.Error()
			} else {
				row.Status = "connected"
				e.mu.Lock()
				if e.lastHave != nil {
					e.lastHave[attach.Key(sc.Host, d.BusID)] = -1 // attached; port not known yet
				}
				e.mu.Unlock()
			}
		default:
			row.Status = "disconnected"
		}
		rows = append(rows, row)
	}
	e.mu.Lock()
	// A click that arrived while we were working wins over what we computed.
	prev := map[string]DeviceState{}
	for _, d := range rt.devices {
		prev[d.BusID] = d
	}
	for i := range rows {
		r := &rows[i]
		key := attach.Key(sc.Host, r.BusID)
		if p, ok := prev[r.BusID]; ok && (e.inflight[key] || e.touched[key].After(began)) {
			r.Status, r.Error, r.Paused = p.Status, p.Error, p.Paused
			continue
		}
		if w := e.want(sc, r.ID); !w && r.Status != "disconnected" && e.held[serverKey(sc)+"|"+r.ID] {
			r.Status, r.Error = "disconnected", ""
			e.wake()
		}
	}
	rt.ok, rt.err, rt.devices = true, "", rows
	e.mu.Unlock()
}

// TrayMenuData describes the tray icon's right-click menu: the groups, and
// each connected server with its devices; any of them can be connected or
// disconnected from there.
type TrayMenuData struct {
	// Groups' Elsewhere counts tell the menu to offer taking them over.
	Groups  []GroupState
	Servers []TrayServer
}

type TrayServer struct {
	Title   string
	Devices []TrayDevice
}

type TrayDevice struct {
	Host, BusID string
	Title       string
	Connected   bool
	Elsewhere   bool // another computer is using it; clicking takes it over
}

// TrayMenu returns the menu contents and a signature that changes exactly
// when the menu needs redrawing.
func (e *Engine) TrayMenu() (TrayMenuData, string) {
	s := e.State()
	var menu TrayMenuData
	var sig strings.Builder
	for _, g := range s.Groups {
		if g.Total == 0 {
			continue
		}
		menu.Groups = append(menu.Groups, g)
		fmt.Fprintf(&sig, "group|%s|%d|%d|%d\n", g.Name, g.Connected, g.Total, g.Elsewhere)
	}
	for _, sv := range s.Servers {
		name := sv.Name
		if name == "" {
			name = strings.TrimSuffix(sv.Host, ":"+proto.DefaultPort)
		}
		ts := TrayServer{Title: name}
		if !sv.OK {
			ts.Title += " (unreachable)"
		}
		for _, d := range sv.Devices {
			if d.Hidden {
				continue
			}
			title := d.Alias
			if title == "" {
				title = d.Name
			}
			if title == "" {
				title = d.Kind + " " + d.ID
			}
			on := d.Status == "connected" || d.Status == "connecting"
			switch {
			case d.Status == "error":
				title += " (failed)"
			case d.Status == "elsewhere":
				title += " (in use on " + d.Elsewhere + ")"
			case d.Status == "kept":
				title += " (kept on the server)"
			case d.Paused:
				title += " (off until restart)"
			}
			ts.Devices = append(ts.Devices, TrayDevice{Host: sv.Host, BusID: d.BusID, Title: title, Connected: on,
				Elsewhere: d.Status == "elsewhere" || d.Status == "kept"})
			fmt.Fprintf(&sig, "%s|%s|%s|%v\n", sv.Host, d.BusID, title, on)
		}
		fmt.Fprintf(&sig, "%s|%s\n", sv.Host, ts.Title)
		menu.Servers = append(menu.Servers, ts)
	}
	return menu, sig.String()
}

// Summary is a one-line status for the tray icon.
func (e *Engine) Summary() string {
	s := e.State()
	if len(s.Servers) == 0 {
		return "PulseUSB: no server connected"
	}
	n := 0
	for _, sv := range s.Servers {
		for _, d := range sv.Devices {
			if d.Status == "connected" {
				n++
			}
		}
	}
	return fmt.Sprintf("PulseUSB: %d device(s) connected", n)
}
