// pulseusb-gui is the desktop app for the machine that uses the devices: it
// keeps chosen devices of pulseusb servers attached, shows a small settings
// window, and can start with the computer, minimized to the tray or in the
// background before anyone signs in.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"pulseusb/internal/attach"
	"pulseusb/internal/client"
	"pulseusb/internal/discover"
	"pulseusb/internal/gui"
	"pulseusb/internal/proto"
)

// The window is served on this fixed local address; it doubles as the
// single-instance lock.
const addr = "127.0.0.1:3241"

type system struct{}

func (system) List(host string) ([]*proto.Device, error) { return client.List(host) }
func (system) Attached() (map[string]int, error)         { return attach.Attached() }
func (system) Attach(host, busid string, once, streaming bool) error {
	return attach.Attach(host, busid, once, streaming)
}
func (system) Detach(port int) error { return attach.Detach(port) }
func (system) Kinds(host string) map[string]string {
	kinds, _ := client.Kinds(host)
	return kinds
}
func (system) Names(host string) map[string]string {
	names, _ := client.Names(host)
	return names
}
func (system) Holders(host string) (map[string]client.Holder, string, error) {
	return client.HoldersAndSelf(host)
}
func (system) Release(host, busid string) error {
	return client.Release(host, busid, proto.ReleaseForMe)
}
func (system) Discover() []discover.Server {
	found, _ := discover.Query(600 * time.Millisecond)
	return found
}

// pointerFile, next to the installed program, tells the boot-time instance
// (which runs as another account) whose settings file to follow.
func pointerFile() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "background.txt")
}

// running asks whatever holds our port which build and kind it is.
func running() (version, kind string) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/api/version")
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	var v struct{ Version, Kind string }
	json.NewDecoder(resp.Body).Decode(&v)
	return v.Version, v.Kind
}

// takeOver stops the instance holding our port and returns the freed
// listener. It first asks politely with the token that instance left on
// disk, then (for builds too old to understand) ends the process.
func takeOver(configPath string) net.Listener {
	if token, err := os.ReadFile(gui.TokenPath(configPath)); err == nil {
		req, _ := http.NewRequest("POST", "http://"+addr+"/api/quit", strings.NewReader("{}"))
		req.Header.Set("X-Token", strings.TrimSpace(string(token)))
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
		}
	}
	for i := 0; i < 40; i++ {
		if i == 10 {
			killOthers()
		}
		if l, err := net.Listen("tcp", addr); err == nil {
			return l
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func main() {
	minimized := flag.Bool("minimized", false, "start without opening the window")
	background := flag.Bool("background", false, "run without window or tray icon (boot-time start)")
	window := flag.Bool("window", false, "show the settings window of the running app and exit when it is closed")
	service := flag.Bool("service", false, "run as a system service: no window; settings page on http://"+addr+"/")
	config := flag.String("config", "", "settings file to use instead of the per-user one")
	if platformCommand(os.Args[1:]) {
		return
	}
	flag.Parse()
	url := "http://" + addr + "/"
	if *window {
		runWindow(url)
		return
	}

	path := gui.DefaultPath()
	if *config != "" {
		path = *config
	}
	if *background {
		// Follow the settings of the user who switched this on.
		data, err := os.ReadFile(pointerFile())
		if err != nil {
			fmt.Fprintln(os.Stderr, "pulseusb-gui: no settings to follow:", err)
			os.Exit(1)
		}
		path = strings.TrimSpace(string(data))
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		version, kind := running()
		switch {
		case *background:
			return // someone is already looking after the devices
		case version == gui.Version && kind == "gui":
			openWindow(url) // already running: just bring up its window
			return
		}
		// A boot-time instance, or an older build: replace it. Attached
		// devices are held by the driver and stay attached meanwhile.
		if l = takeOver(path); l == nil {
			openWindow(url)
			return
		}
	}

	e := &gui.Engine{Backend: backend(), Path: path, Kind: "gui",
		Modes:  runtime.GOOS == "windows" || os.Getenv("PULSEUSB_DEMO") != "",
		OnQuit: func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }}
	if *background {
		e.Kind, e.ReadOnly = "background", true
	} else if *service {
		// Started by the system, not by a person: nothing to launch at
		// sign-in and nothing to install.
	} else {
		e.Autostart, e.Setup = autostart{}, newSetup()
		e.Hotkeys = runtime.GOOS == "windows"
		refreshInstalledCopy()
	}
	if !*background {
		migrateOldName(path)
	}
	e.Load()
	if e.Hotkeys {
		runHotkeys(e)
		guardStuckKeys(e)
	}
	stop := make(chan struct{})
	go e.Run(stop)
	go func() {
		if err := http.Serve(l, e.Handler()); err != nil {
			fmt.Fprintln(os.Stderr, "pulseusb-gui:", err)
		}
	}()
	if *background || *service {
		select {}
	}
	// Keep the boot-time start's pointer to the settings fresh; and if the
	// boot-time start itself is broken, show the window so the notice about
	// it is seen even when the app normally starts minimized.
	e.PrepareBackground()
	if (!*minimized && !e.Config().StartMinimized) || e.BackgroundStale() {
		openWindow(url)
	}
	runTray(e, func() { openWindow(url) })
	close(stop)
}
