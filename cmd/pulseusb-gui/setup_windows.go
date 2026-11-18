package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"pulseusb/internal/attach"
	"pulseusb/internal/gui"
)

type setup struct{}

func newSetup() gui.Setup { return setup{} }

func (setup) DriverInstalled() bool { return attach.DriverInstalled() }

// InstallDriver downloads the newest usbip-win2 installer from the
// project's GitHub releases and runs it silently. The installer is that
// project's own; Windows asks for administrator rights once.
func (setup) InstallDriver(report func(string)) error {
	// Use the copy that came with PulseUSB's own setup if there is one;
	// otherwise fetch the latest from the usbip-win2 project.
	path, bundled := bundledDriver(), true
	if path == "" {
		bundled = false
		report("Looking up the latest usbip-win2 release…")
		url, name, err := latestInstaller()
		if err != nil {
			return err
		}
		report("Downloading " + name + "…")
		path = filepath.Join(os.TempDir(), name)
		if err := download(url, path); err != nil {
			return err
		}
	}
	report("Installing the driver. Windows asks for permission once.")
	// Run usbip-win2's own installer without its wizard: driver and tools
	// only, no restart. It needs administrator rights, hence the prompt.
	code, err := runElevated(path, `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /TYPE=compact`)
	if !bundled {
		os.Remove(path)
	}
	if err != nil {
		return fmt.Errorf("Windows did not allow the installation: %v", err)
	}
	if code != 0 {
		return fmt.Errorf("the driver installer ended with code %d", code)
	}
	if !attach.DriverInstalled() {
		return fmt.Errorf("the driver installer finished, but its tool was not found")
	}
	report("Driver installed.")
	return nil
}

// shellExecuteInfo is Windows' SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated starts a program with administrator rights (Windows shows
// its permission prompt), waits for it and returns its exit code.
func runElevated(file, args string) (uint32, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	filep, _ := windows.UTF16PtrFromString(file)
	argp, _ := windows.UTF16PtrFromString(args)
	info := shellExecuteInfo{
		mask:       0x40, // SEE_MASK_NOCLOSEPROCESS: give us the process to wait on
		verb:       verb,
		file:       filep,
		parameters: argp,
		show:       windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, err
	}
	if info.process == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(info.process)
	windows.WaitForSingleObject(info.process, 15*60*1000)
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// bundledDriver returns the usbip-win2 installer shipped inside PulseUSB's
// setup program, if this installation has one.
func bundledDriver() string {
	dirs := []string{filepath.Dir(installedExe())}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, d := range dirs {
		if p := filepath.Join(d, "driver", "usbip-win2-setup.exe"); fileExists(p) {
			return p
		}
	}
	return ""
}

const releasesURL = "https://api.github.com/repos/vadimgrn/usbip-win2/releases?per_page=10"

func latestInstaller() (url, name string, err error) {
	req, _ := http.NewRequest("GET", releasesURL, nil)
	req.Header.Set("User-Agent", "PulseUSB")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var releases []struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", "", err
	}
	want := "x64"
	if arch := os.Getenv("PROCESSOR_ARCHITEW6432") + os.Getenv("PROCESSOR_ARCHITECTURE"); strings.Contains(strings.ToUpper(arch), "ARM64") {
		want = "arm64"
	}
	for _, r := range releases { // newest first
		for _, a := range r.Assets {
			n := strings.ToLower(a.Name)
			if strings.HasSuffix(n, ".exe") && strings.Contains(n, want) && !strings.Contains(n, "debug") &&
				strings.HasPrefix(a.URL, "https://github.com/vadimgrn/usbip-win2/") {
				return a.URL, filepath.Base(a.Name), nil
			}
		}
	}
	return "", "", fmt.Errorf("no installer found; get it from github.com/vadimgrn/usbip-win2/releases")
}

func download(url, path string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path + ".part")
		return err
	}
	os.Remove(path)
	return os.Rename(path+".part", path)
}

// installedExe is where the app lives once installed: per user, so no
// administrator rights are needed.
func installedExe() string {
	// The setup program records the folder the user chose.
	if k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Uninstall\PulseUSB`, registry.QUERY_VALUE); err == nil {
		dir, _, err := k.GetStringValue("InstallLocation")
		k.Close()
		if err == nil && dir != "" {
			if exe := filepath.Join(dir, "PulseUSB.exe"); fileExists(exe) {
				return exe
			}
		}
	}
	return filepath.Join(os.Getenv("LocalAppData"), "Programs", "PulseUSB", "PulseUSB.exe")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (setup) AppInstalled() bool {
	if os.Getenv("LocalAppData") == "" {
		return true // nowhere to install to
	}
	_, err := os.Stat(installedExe())
	return err == nil
}

func (setup) InstallApp() error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	dst := installedExe()
	if !strings.EqualFold(src, dst) {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			return err
		}
	}
	// Start Menu shortcut, through the Windows scripting shell.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	lnk := filepath.Join(os.Getenv("AppData"), "Microsoft", "Windows", "Start Menu", "Programs", "PulseUSB.lnk")
	script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut(" + quote(lnk) + ");$s.TargetPath=" + quote(dst) + ";$s.Save()"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("app copied, but the Start menu entry failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
