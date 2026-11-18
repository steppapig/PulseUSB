package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const windowTitle = "PulseUSB"

func init() {
	// Windows delivers a window's messages to the thread that created it;
	// keep main on one thread for the tray icon and the window.
	runtime.LockOSThread()
}

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procFindWindow            = user32.NewProc("FindWindowW")
	procSetForeground         = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procIsVisible             = user32.NewProc("IsWindowVisible")
	swRestore         uintptr = 9
	swHide            uintptr = 0
)

// focusExisting brings an already open PulseUSB window to the front.
func focusExisting() bool {
	class, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(windowTitle)
	hwnd, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	// The window may be minimized, or hidden in the tray.
	iconic, _, _ := procIsIconic.Call(hwnd)
	visible, _, _ := procIsVisible.Call(hwnd)
	if iconic != 0 || visible == 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForeground.Call(hwnd)
	return true
}

// runWindow shows the settings page in a window of our own (with our icon
// on the title bar and taskbar), using the WebView2 component that ships
// with Windows 10 and 11. It returns when the window is closed. If WebView2
// is not available it falls back to a browser window.
func runWindow(url string) {
	if focusExisting() {
		return
	}
	data := filepath.Join(os.Getenv("LocalAppData"), "PulseUSB", "WebView2")
	os.MkdirAll(data, 0o755)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  data,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  windowTitle,
			Width:  660,
			Height: 900,
			IconId: 1, // the icon embedded in the exe
			Center: true,
		},
	})
	if w == nil {
		openInBrowser(url)
		return
	}
	defer w.Destroy()
	w.Navigate(url)
	go watchWindow(w, url)
	w.Run()
}

// watchWindow hides a minimized window from the taskbar when "minimize to
// tray" is on (a click on the tray icon brings it back), and closes the
// window when the app behind it has gone.
func watchWindow(w webview2.WebView, url string) {
	hwnd := uintptr(w.Window())
	client := &http.Client{Timeout: 2 * time.Second}
	toTray, misses := true, 0
	theme := "?" // not applied yet
	for n := 0; ; n++ {
		time.Sleep(200 * time.Millisecond)
		if n%10 == 0 { // every two seconds, ask the app
			resp, err := client.Get(url + "api/prefs")
			if err != nil {
				if misses++; misses >= 3 {
					w.Dispatch(w.Terminate)
					return
				}
			} else {
				var prefs struct {
					MinimizeToTray bool
					Theme          string
				}
				if json.NewDecoder(resp.Body).Decode(&prefs) == nil {
					toTray = prefs.MinimizeToTray
					if want := resolveTheme(prefs.Theme); want != theme {
						theme = want
						styleTitleBar(hwnd, theme == "dark")
					}
				}
				resp.Body.Close()
				misses = 0
			}
		}
		if !toTray {
			continue
		}
		iconic, _, _ := procIsIconic.Call(hwnd)
		visible, _, _ := procIsVisible.Call(hwnd)
		if iconic != 0 && visible != 0 {
			procShowWindow.Call(hwnd, swHide)
		}
	}
}

// openWindow starts the window as a separate process of this program, so
// the tray icon and the device handling never wait on it.
func openWindow(url string) {
	exe, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exe, "-window")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		if cmd.Start() == nil {
			go cmd.Wait()
			return
		}
	}
	openInBrowser(url)
}

// resolveTheme turns the user's choice into "dark" or "light"; an empty
// choice follows the Windows app theme.
func resolveTheme(choice string) string {
	if choice == "dark" || choice == "light" {
		return choice
	}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return "light"
	}
	defer k.Close()
	if light, _, err := k.GetIntegerValue("AppsUseLightTheme"); err == nil && light == 0 {
		return "dark"
	}
	return "light"
}

var procDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// styleTitleBar makes the window's title bar match the page below it, so
// the two read as one surface: dark or light buttons and text (Windows 10
// and 11), and on Windows 11 the page's exact background colour.
func styleTitleBar(hwnd uintptr, dark bool) {
	const (
		useImmersiveDarkMode = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE
		captionColor         = 35 // DWMWA_CAPTION_COLOR (Windows 11)
		textColor            = 36 // DWMWA_TEXT_COLOR (Windows 11)
	)
	set := func(attr uintptr, value uint32) {
		procDwmSetWindowAttribute.Call(hwnd, attr, uintptr(unsafe.Pointer(&value)), 4)
	}
	// Colours are 0x00BBGGRR; they are the page's --bg and --text.
	if dark {
		set(useImmersiveDarkMode, 1)
		set(captionColor, 0x001b1714)
		set(textColor, 0x00ede9e6)
	} else {
		set(useImmersiveDarkMode, 0)
		set(captionColor, 0x00f7f5f4)
		set(textColor, 0x00241f1b)
	}
}
