package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"

	"pulseusb/internal/attach"
	"pulseusb/internal/gui"
)

//go:embed icon.ico
var icon []byte

// runTray shows the tray icon and blocks until the user quits. Left click
// opens the window; right click lists each server with its devices, where a
// click connects or disconnects one. Devices stay attached after quitting;
// the driver holds them, not this app.
func runTray(e *gui.Engine, open func()) {
	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTooltip("PulseUSB")
		systray.SetOnTapped(open)
		var stop chan struct{} // ends the click handlers of the previous menu
		build := func(menu gui.TrayMenuData) {
			if stop != nil {
				close(stop)
			}
			stop = make(chan struct{})
			done := stop
			systray.ResetMenu()
			onClick := func(item *systray.MenuItem, fn func()) {
				go func() {
					for {
						select {
						case <-item.ClickedCh:
							fn()
						case <-done:
							return
						}
					}
				}()
			}
			onClick(systray.AddMenuItem("Open PulseUSB", "Show the window"), open)
			systray.AddSeparator()
			for _, g := range menu.Groups {
				g := g
				// One entry per group: it connects what is missing, or once
				// everything is connected, disconnects the group.
				allOn := g.Connected == g.Total
				switch {
				case allOn:
					onClick(systray.AddMenuItem(fmt.Sprintf("Disconnect all: %s (%d of %d connected)", g.Name, g.Connected, g.Total), ""),
						func() { e.SetGroupConnected(g.Name, false) })
				case g.Elsewhere > 0:
					onClick(systray.AddMenuItem(fmt.Sprintf("Take over all: %s (%d in use elsewhere)", g.Name, g.Elsewhere), ""),
						func() { e.TakeOverGroup(g.Name) })
				default:
					onClick(systray.AddMenuItem(fmt.Sprintf("Connect all: %s (%d of %d connected)", g.Name, g.Connected, g.Total), ""),
						func() { e.SetGroupConnected(g.Name, true) })
				}
				if g.Connected > 0 && !allOn { // partly connected: offer the other direction too
					onClick(systray.AddMenuItem("Disconnect all: "+g.Name, ""), func() { e.SetGroupConnected(g.Name, false) })
				}
			}
			if len(menu.Groups) > 0 {
				systray.AddSeparator()
			}
			if len(menu.Servers) == 0 {
				systray.AddMenuItem("No server connected", "").Disable()
			}
			for _, sv := range menu.Servers {
				srv := systray.AddMenuItem(sv.Title, "")
				if len(sv.Devices) == 0 {
					srv.AddSubMenuItem("No devices shared", "").Disable()
				}
				for _, d := range sv.Devices {
					d := d
					tip, action := "Click to connect", func() { e.SetConnected(d.Host, d.BusID, true) }
					switch {
					case d.Connected:
						tip, action = "Click to disconnect", func() { e.SetConnected(d.Host, d.BusID, false) }
					case d.Elsewhere:
						tip, action = "Click to take it over", func() { e.TakeOver(d.Host, d.BusID) }
					}
					onClick(srv.AddSubMenuItemCheckbox(d.Title, tip, d.Connected), action)
				}
			}
			systray.AddSeparator()
			onClick(systray.AddMenuItem("Release stuck keys", "If Windows acts as if Win, Ctrl, Alt or Shift is held, this lets them go"), func() {
				if keys := releaseStuckKeys(); len(keys) > 0 {
					e.Note("Released stuck keys: %s", strings.Join(keys, "+"))
				} else {
					e.Note("Release stuck keys: none were held")
				}
			})
			systray.AddSeparator()
			onClick(systray.AddMenuItem("Quit", "Stop PulseUSB (connected devices stay connected)"), systray.Quit)
		}
		menu, sig := e.TrayMenu()
		build(menu)
		go func() {
			for range time.Tick(time.Second) {
				systray.SetTooltip(e.Summary())
				if menu, now := e.TrayMenu(); now != sig {
					sig = now
					build(menu)
				}
			}
		}()
	}, func() {})
}

// openInBrowser is the fallback when our own window cannot be created: the
// page as a chromeless app window in Edge, failing that the default browser.
func openInBrowser(url string) {
	for _, dir := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
		if dir == "" {
			continue
		}
		edge := filepath.Join(dir, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(edge); err == nil {
			if exec.Command(edge, "--app="+url, "--window-size=600,720").Start() == nil {
				return
			}
		}
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "PulseUSB"
	taskName = "PulseUSB-background"

	// Names from when the app was called usbeth, cleaned up on first run.
	oldRunValue = "usbeth"
	oldTaskName = "usbeth-background"
	oldExeName  = "usbeth-gui.exe"
)

// platformCommand handles extra sub-commands. On Windows there is one,
// used by the setup program: "-install-driver" installs the usbip-win2
// driver if it is missing, and exits with 0 on success.
func platformCommand(args []string) bool {
	if len(args) == 0 || args[0] != "-install-driver" {
		return false
	}
	if !attach.DriverInstalled() {
		if err := (setup{}).InstallDriver(func(string) {}); err != nil {
			os.Exit(1)
		}
	}
	return true
}

// killOthers ends other running copies of the app; used only to replace a
// build too old to be asked to quit.
func killOthers() {
	for _, name := range []string{"PulseUSB.exe", oldExeName} {
		cmd := exec.Command("taskkill", "/F", "/IM", name, "/FI", "PID ne "+strconv.Itoa(os.Getpid()))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		cmd.Run()
	}
}

// refreshInstalledCopy keeps the installed program in step with the build
// being run, so that the next start with the computer uses this build.
func refreshInstalledCopy() {
	src, err := os.Executable()
	dst := installedExe()
	if err != nil || strings.EqualFold(src, dst) {
		return
	}
	old, err := os.Stat(dst)
	if err != nil {
		return // not installed
	}
	if cur, err := os.Stat(src); err == nil && cur.Size() == old.Size() && !cur.ModTime().After(old.ModTime()) {
		return
	}
	if data, err := os.ReadFile(src); err == nil {
		os.WriteFile(dst, data, 0o755)
	}
}

func (autostart) BackgroundSupported() bool { return true }

// SetBackground adds or removes a Windows scheduled task that starts the
// installed program at boot, before anyone signs in, under the system
// account. Creating it needs administrator rights, so Windows asks once.
func (a autostart) SetBackground(on bool, configPath string) error {
	// The commands go in a small batch file so that one permission prompt
	// covers them all, including removal of the task under the old name.
	script := "@echo off\r\nschtasks /Delete /TN " + oldTaskName + " /F >nul 2>&1\r\n"
	if !on {
		script += "schtasks /Delete /TN " + taskName + " /F >nul 2>&1\r\nexit /b 0\r\n"
	}
	if on {
		exe := installedExe()
		if _, err := os.Stat(exe); err != nil {
			return fmt.Errorf("install the app on this PC first")
		}
		if err := a.PrepareBackground(configPath); err != nil {
			return err
		}
		script += `schtasks /Create /TN ` + taskName + ` /TR "\"` + strings.ReplaceAll(exe, "%", "%%") +
			`\" -background" /SC ONSTART /RU SYSTEM /RL HIGHEST /F` + "\r\nexit /b %errorlevel%\r\n"
	}
	batch := filepath.Join(os.TempDir(), "pulseusb-task.cmd")
	if err := os.WriteFile(batch, []byte(script), 0o644); err != nil {
		return err
	}
	defer os.Remove(batch)
	// Wait for the result, so a failure is reported instead of assumed away.
	code, err := runElevated("cmd.exe", `/c "`+batch+`"`)
	if err != nil {
		return fmt.Errorf("Windows did not allow the change: %v", err)
	}
	if code != 0 {
		return fmt.Errorf("Windows could not create the start-at-boot task (code %d)", code)
	}
	return nil
}

// BackgroundTarget is the program the boot-time task launches.
func (autostart) BackgroundTarget() string { return installedExe() }

// PrepareBackground writes, next to the installed program, where the
// boot-time instance finds the user's settings.
func (autostart) PrepareBackground(configPath string) error {
	pointer := filepath.Join(filepath.Dir(installedExe()), "background.txt")
	if cur, err := os.ReadFile(pointer); err == nil && string(cur) == configPath {
		return nil
	}
	return os.WriteFile(pointer, []byte(configPath), 0o644)
}

// autostart uses the per-user Run key, so it needs no administrator rights.
type autostart struct{}

func (autostart) Supported() bool { return true }

func (autostart) Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.TrimSpace(v) != ""
}

func (autostart) Set(enabled, minimized bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := os.Stat(installedExe()); err == nil {
		exe = installedExe() // survive the download folder being cleaned
	}
	cmd := `"` + exe + `"`
	if minimized {
		cmd += " -minimized"
	}
	return k.SetStringValue(runValue, cmd)
}

// migrateOldName moves an installation from when the app was called usbeth:
// its settings, its launch-at-startup entry, its installed copy and its
// Start menu entry. newConfig is the settings file of this build.
func migrateOldName(newConfig string) {
	oldConfig := filepath.Join(filepath.Dir(filepath.Dir(newConfig)), "usbeth", "config.json")
	if _, err := os.Stat(newConfig); err != nil {
		if data, err := os.ReadFile(oldConfig); err == nil {
			// The boot-time task still points at the old program; have the
			// user switch that option on again under the new name.
			data = []byte(strings.Replace(string(data), `"background": true`, `"background": false`, 1))
			os.MkdirAll(filepath.Dir(newConfig), 0o755)
			os.WriteFile(newConfig, data, 0o644)
		}
	}
	oldDir := filepath.Join(os.Getenv("LocalAppData"), "Programs", "usbeth")
	if _, err := os.Stat(oldDir); err == nil {
		// Give the installed copy a new home before removing the old one.
		if _, err := os.Stat(installedExe()); err != nil {
			setup{}.InstallApp()
		}
		os.RemoveAll(oldDir)
	}
	os.Remove(filepath.Join(os.Getenv("AppData"), "Microsoft", "Windows", "Start Menu", "Programs", "usbeth.lnk"))
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if old, _, err := k.GetStringValue(oldRunValue); err == nil {
			k.DeleteValue(oldRunValue)
			k.Close()
			autostart{}.Set(true, strings.Contains(old, "-minimized"))
		} else {
			k.Close()
		}
	}
}
