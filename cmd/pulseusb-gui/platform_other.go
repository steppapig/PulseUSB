//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"pulseusb/internal/gui"
)

// runTray: there is no tray icon outside Windows; run until interrupted.
func runTray(e *gui.Engine, open func()) {
	fmt.Println("pulseusb-gui running; window at http://" + addr + "/")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func killOthers()           {}
func migrateOldName(string) {}
func refreshInstalledCopy() {}

// autostart writes a desktop-session autostart entry.
type autostart struct{}

func (autostart) path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "autostart", "pulseusb.desktop")
}

func (a autostart) Supported() bool { return a.path() != "" }

func (a autostart) Enabled() bool {
	_, err := os.Stat(a.path())
	return err == nil
}

func (a autostart) Set(enabled, minimized bool) error {
	if !enabled {
		if err := os.Remove(a.path()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := `"` + exe + `"`
	if minimized {
		cmd += " -minimized"
	}
	os.MkdirAll(filepath.Dir(a.path()), 0o755)
	return os.WriteFile(a.path(), []byte("[Desktop Entry]\nType=Application\nName=PulseUSB\nExec="+cmd+"\n"), 0o644)
}

// Starting before sign-in is only offered on Windows (or in the demo).
func (autostart) BackgroundSupported() bool        { return os.Getenv("PULSEUSB_DEMO") != "" }
func (autostart) SetBackground(bool, string) error { return nil }
func (autostart) BackgroundTarget() string         { return os.Getenv("PULSEUSB_DEMO_TARGET") }
func (autostart) PrepareBackground(string) error   { return nil }

// demoSetup lets the install prompts be tried with PULSEUSB_DEMO=setup.
type demoSetup struct{ driver, app bool }

func (d *demoSetup) DriverInstalled() bool { return d.driver }
func (d *demoSetup) InstallDriver(report func(string)) error {
	report("Downloading the driver installer…")
	time.Sleep(1500 * time.Millisecond)
	d.driver = true
	return nil
}
func (d *demoSetup) AppInstalled() bool { return d.app }
func (d *demoSetup) InstallApp() error  { d.app = true; return nil }

func newSetup() gui.Setup {
	if os.Getenv("PULSEUSB_DEMO") == "setup" {
		return &demoSetup{}
	}
	return nil
}
