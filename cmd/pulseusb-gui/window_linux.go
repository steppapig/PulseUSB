package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"

	"github.com/ebitengine/purego"
)

// The Linux window: a GTK window holding a WebKitGTK view of the settings
// page, so the app has its own window instead of a browser tab. The
// libraries are loaded at run time, which keeps the program a single static
// file that also builds for other architectures.

func init() {
	// GTK must be driven from the thread that initialised it.
	runtime.LockOSThread()
}

// windowLibsHint says how to get the libraries the window needs.
const windowLibsHint = `PulseUSB needs GTK 3 and WebKitGTK to show its window. Install them with:
  Arch:           sudo pacman -S webkit2gtk-4.1
  Debian/Ubuntu:  sudo apt install libwebkit2gtk-4.1-0
  Fedora:         sudo dnf install webkit2gtk4.1`

type gtkLibs struct{ gtk, webkit, gobject, glib uintptr }

func loadWindowLibs() (*gtkLibs, error) {
	open := func(names ...string) (uintptr, error) {
		var err error
		for _, n := range names {
			var h uintptr
			if h, err = purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
				return h, nil
			}
		}
		return 0, err
	}
	var l gtkLibs
	var err error
	if l.glib, err = open("libglib-2.0.so.0"); err != nil {
		return nil, err
	}
	if l.gobject, err = open("libgobject-2.0.so.0"); err != nil {
		return nil, err
	}
	if l.gtk, err = open("libgtk-3.so.0"); err != nil {
		return nil, err
	}
	if l.webkit, err = open("libwebkit2gtk-4.1.so.0", "libwebkit2gtk-4.0.so.37"); err != nil {
		return nil, err
	}
	return &l, nil
}

// windowSocket is how a second launch asks the open window to come forward.
func windowSocket() string { return fmt.Sprintf("@pulseusb-window-%d", os.Getuid()) }

// runWindow shows the window and returns when it is closed.
func runWindow(url string) {
	// Already open? Ask that window to come to the front instead.
	if c, err := net.Dial("unix", windowSocket()); err == nil {
		c.Close()
		return
	}
	libs, err := loadWindowLibs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pulseusb: %v\n\n%s\n", err, windowLibsHint)
		os.Exit(1)
	}
	// WebKitGTK's GPU path shows an empty window with some graphics
	// drivers; the plain path works everywhere and is plenty for this page.
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}

	var (
		gSetPrgname     func(string)
		gtkInitCheck    func(argc, argv uintptr) bool
		gtkWindowNew    func(kind int32) uintptr
		gtkSetTitle     func(win uintptr, title string)
		gtkSetSize      func(win uintptr, w, h int32)
		gtkSetIconName  func(win uintptr, name string)
		gtkContainerAdd func(container, widget uintptr)
		gtkShowAll      func(widget uintptr)
		gtkPresent      func(win uintptr)
		gtkMain         func()
		gtkMainQuit     func()
		webViewNew      func() uintptr
		webViewLoadURI  func(view uintptr, uri string)
		gSignalConnect  func(instance uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64
		gIdleAdd        func(fn, data uintptr) uint32
	)
	purego.RegisterLibFunc(&gSetPrgname, libs.glib, "g_set_prgname")
	purego.RegisterLibFunc(&gIdleAdd, libs.glib, "g_idle_add")
	purego.RegisterLibFunc(&gSignalConnect, libs.gobject, "g_signal_connect_data")
	purego.RegisterLibFunc(&gtkInitCheck, libs.gtk, "gtk_init_check")
	purego.RegisterLibFunc(&gtkWindowNew, libs.gtk, "gtk_window_new")
	purego.RegisterLibFunc(&gtkSetTitle, libs.gtk, "gtk_window_set_title")
	purego.RegisterLibFunc(&gtkSetSize, libs.gtk, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&gtkSetIconName, libs.gtk, "gtk_window_set_icon_name")
	purego.RegisterLibFunc(&gtkContainerAdd, libs.gtk, "gtk_container_add")
	purego.RegisterLibFunc(&gtkShowAll, libs.gtk, "gtk_widget_show_all")
	purego.RegisterLibFunc(&gtkPresent, libs.gtk, "gtk_window_present")
	purego.RegisterLibFunc(&gtkMain, libs.gtk, "gtk_main")
	purego.RegisterLibFunc(&gtkMainQuit, libs.gtk, "gtk_main_quit")
	purego.RegisterLibFunc(&webViewNew, libs.webkit, "webkit_web_view_new")
	purego.RegisterLibFunc(&webViewLoadURI, libs.webkit, "webkit_web_view_load_uri")

	gSetPrgname("pulseusb") // names the window class and the page's storage folder
	if !gtkInitCheck(0, 0) {
		fmt.Fprintln(os.Stderr, "pulseusb: no graphical session to show the window in")
		os.Exit(1)
	}
	win := gtkWindowNew(0) // top-level window
	gtkSetTitle(win, "PulseUSB")
	gtkSetSize(win, 660, 900)
	gtkSetIconName(win, "pulseusb")
	view := webViewNew()
	gtkContainerAdd(win, view)
	webViewLoadURI(view, url)
	gSignalConnect(win, "destroy", purego.NewCallback(func(uintptr, uintptr) uintptr {
		gtkMainQuit()
		return 0
	}), 0, 0, 0)

	// A later launch connects to this socket; bring the window forward then.
	if l, err := net.Listen("unix", windowSocket()); err == nil {
		present := purego.NewCallback(func(uintptr) uintptr {
			gtkPresent(win)
			return 0 // run once
		})
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
				gIdleAdd(present, 0) // GTK may only be touched from its own thread
			}
		}()
	}
	gtkShowAll(win)
	gtkMain()
}

// openWindow starts the window as a separate process of this program, so
// the device handling never waits on it.
func openWindow(url string) {
	if exe, err := os.Executable(); err == nil {
		cmd := exec.Command(exe, "-window")
		cmd.Stderr = os.Stderr
		if cmd.Start() == nil {
			go cmd.Wait()
			return
		}
	}
	fmt.Fprintln(os.Stderr, "pulseusb: could not open the window; the page is at", url)
}

// windowLibsMissing reports, for the installer, whether the window could
// not be shown on this machine.
func windowLibsMissing() bool {
	_, err := loadWindowLibs()
	return err != nil
}
