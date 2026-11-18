package gui

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"pulseusb/internal/licenses"
)

//go:embed page.html
var page string

//go:embed icon.png
var iconPNG []byte

//go:embed icon.ico
var iconICO []byte

// IconPNG returns the app's logo, for desktop launchers.
func IconPNG() []byte { return iconPNG }

// TokenPath is where a running instance leaves the secret that lets a newer
// instance of the same user ask it to stop.
func TokenPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "token")
}

// Handler serves the window and its API. Requests must come from this
// machine and carry the token embedded in the page, so other web sites open
// in the user's browser cannot drive the app.
func (e *Engine) Handler() http.Handler {
	raw := make([]byte, 16)
	rand.Read(raw)
	token := hex.EncodeToString(raw)
	if e.Path != "" {
		os.MkdirAll(filepath.Dir(e.Path), 0o755)
		os.WriteFile(TokenPath(e.Path), []byte(token), 0o600)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r) // (icons are registered separately)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// The theme goes into the page itself so it never flashes the wrong colours.
		html := strings.Replace(page, "__TOKEN__", token, 1)
		w.Write([]byte(strings.Replace(html, "__THEME__", e.Config().Theme, 1)))
	})
	// The logo: shown in the page, and used by the browser's app window as
	// its title-bar and taskbar icon.
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(iconICO)
	})
	// The licences of the third-party work in this program.
	mux.HandleFunc("/licenses.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(licenses.Text))
	})
	// Which build is running, so a newer one knows to take over.
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": Version, "kind": e.Kind})
	})
	// What the window process needs to know; nothing sensitive.
	mux.HandleFunc("/api/prefs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		c := e.Config()
		json.NewEncoder(w).Encode(map[string]any{"minimizeToTray": !c.KeepWindow, "theme": c.Theme})
	})
	reply := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(e.State())
	}
	// post registers an action taking a JSON body.
	post := func(path string, in any, fn func() error) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if in != nil {
				reflect.ValueOf(in).Elem().SetZero() // targets are reused between requests
			}
			if r.Method != http.MethodPost || (in != nil && json.NewDecoder(r.Body).Decode(in) != nil) {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := fn(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			e.changed() // other open windows should see it too
			reply(w)
		})
	}
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		reply(w)
	})
	// The window's update channel: answers the moment something changes,
	// or after a second so that slow-moving details stay fresh too.
	mux.HandleFunc("/api/wait", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		e.Wait(since, time.Second)
		reply(w)
	})
	var srv struct{ Host, Name string }
	post("/api/server/add", &srv, func() error { e.AddServer(srv.Host, strings.TrimSpace(srv.Name)); return nil })
	post("/api/server/remove", &srv, func() error { e.RemoveServer(srv.Host); return nil })
	var dev struct {
		Host, BusID, Mode, Alias, Group string
		On                              bool
	}
	post("/api/device/takeover", &dev, func() error { e.TakeOver(dev.Host, dev.BusID); return nil })
	post("/api/group/takeover", &dev, func() error { e.TakeOverGroup(dev.Group); return nil })
	var sh struct{ Target, Combo string }
	post("/api/shortcut", &sh, func() error { return e.SetShortcut(sh.Target, sh.Combo) })
	post("/api/device/connect", &dev, func() error { e.SetConnected(dev.Host, dev.BusID, dev.On); return nil })
	post("/api/device/auto", &dev, func() error { e.SetAuto(dev.Host, dev.BusID, dev.On); return nil })
	post("/api/device/mode", &dev, func() error { e.SetMode(dev.Host, dev.BusID, dev.Mode); return nil })
	post("/api/device/hide", &dev, func() error { e.SetHidden(dev.Host, dev.BusID, dev.On); return nil })
	post("/api/device/alias", &dev, func() error { e.SetAlias(dev.Host, dev.BusID, dev.Alias); return nil })
	post("/api/device/group", &dev, func() error { e.SetGroup(dev.Host, dev.BusID, dev.Group); return nil })
	var ren struct{ Group, Name, Theme string }
	post("/api/group/rename", &ren, func() error { e.RenameGroup(ren.Group, ren.Name); return nil })
	post("/api/theme", &ren, func() error { e.SetTheme(ren.Theme); return nil })
	post("/api/group/delete", &dev, func() error { e.DeleteGroup(dev.Group); return nil })
	post("/api/group/connect", &dev, func() error { e.SetGroupConnected(dev.Group, dev.On); return nil })
	var st Startup
	post("/api/startup", &st, func() error { return e.SetStartup(st) })
	post("/api/startup/repair", nil, e.RepairBackground)
	post("/api/driver", nil, func() error { e.InstallDriver(); return nil })
	post("/api/install", nil, e.InstallApp)
	post("/api/quit", nil, func() error { e.Quit(); return nil })

	one := make(chan struct{}, 1) // actions share decode targets, so run one at a time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			one <- struct{}{}
			defer func() { <-one }()
		}
		mux.ServeHTTP(w, r)
	})
}
