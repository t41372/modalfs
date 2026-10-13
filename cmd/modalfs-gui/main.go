// modalfs-gui is the desktop window (Wails: the system WebView, so the app
// stays small). It is only a client: mounts live in the background daemon,
// which this binary can also run (`modalfs-gui daemon`), so closing the
// window never unmounts anything.
//
// Build with `just app` (macOS, needs the Xcode command line tools) or
// `just linux-gui`. By hand:
//
//	go build -tags desktop,production -ldflags "-s -w" -o modalfs-gui ./cmd/modalfs-gui
//
// On Linux add the webkit2_41 tag when only WebKitGTK 4.1 is installed.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
	"github.com/t41372/modalfs/internal/webui"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed icon.png
var icon []byte

// App holds the methods the UI can call (window.go.main.App.*).
type App struct {
	ctx      context.Context
	client   *daemon.Client
	quitting atomic.Bool // the window may close for good now
	acked    atomic.Bool // the UI answered the last quit request
	ran      atomic.Bool // a daemon was running for this window at some point
	trayOnce sync.Once
}

// theApp is the one App, for the callbacks from native_darwin.m.
var theApp *App

// SetAppearance makes the window light, dark, or follow the system ("system"),
// so the glass behind the UI matches the appearance chosen in Settings.
func (*App) SetAppearance(name string) { setAppearance(name) }

// Quitting is called by the UI right before it posts /api/shutdown. From then
// on a failed request never starts a new daemon, which would mount again, and
// the window may close for good.
func (a *App) Quitting() { a.quitting.Store(true) }

// QuitAck tells the app the UI is showing the quit checklist it asked for.
func (a *App) QuitAck() { a.acked.Store(true) }

// SaveText asks where to save a text file, such as an encrypted volume's key
// (the WebView can't download), and writes it readable only by its owner. It
// returns the path, or "" when the person cancelled.
func (a *App) SaveText(name, text string) (string, error) {
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{DefaultFilename: name, CanCreateDirectories: true})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("couldn't save %s: %w", path, err)
	}
	return path, nil
}

// requestQuit is ⌘Q, Quit in the menu bar menu, or Quit in the Dock: the
// window comes forward and shows the quit checklist, which unmounts every
// volume and then quits. A window that doesn't answer (its page failed to
// load) can't show it, so the app quits without it; the daemon keeps the
// mounts, as when the window just closes.
func (a *App) requestQuit() {
	if a.quitting.Load() || a.ctx == nil {
		return
	}
	nativeShowWindow()
	a.acked.Store(false)
	wruntime.EventsEmit(a.ctx, "quit:request")
	time.Sleep(4 * time.Second)
	if !a.acked.Load() && !a.quitting.Load() {
		a.quitting.Store(true)
		wruntime.Quit(a.ctx)
	}
}

// beforeClose runs when Wails is about to quit. Unless the quit checklist
// finished (or it's a logout), it shows the checklist instead.
func (a *App) beforeClose(context.Context) (prevent bool) {
	if a.quitting.Load() {
		// Wails stops the run loop from a goroutine here; an event makes it notice.
		go func() {
			for range 20 {
				time.Sleep(100 * time.Millisecond)
				nativeWake()
			}
		}()
		return false
	}
	go a.requestQuit()
	return true
}

// nativeAction is a click in the menu bar menu or the app menu.
func (a *App) nativeAction(action, id string) {
	nav := func(section, id string) {
		nativeShowWindow()
		wruntime.EventsEmit(a.ctx, "nav", section, id)
	}
	switch action {
	case "reveal":
		if err := a.client.Do("POST", "/api/mounts/"+url.PathEscape(id)+"/reveal", nil, nil); err != nil {
			nav("volumes", id) // the page says what's wrong
		}
	case "mount":
		if err := a.client.Do("POST", "/api/mounts/"+url.PathEscape(id)+"/start", nil, nil); err != nil {
			nav("volumes", id)
		}
	case "volume":
		nav("volumes", id)
	case "downloads":
		nav("downloads", "")
	case "settings":
		nav("settings", "")
	case "open":
		nativeShowWindow()
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if hasTray {
		nativeInit()
	}
}

func (a *App) domReady(ctx context.Context) {
	styleWindow()
	if hasTray {
		a.trayOnce.Do(func() { go a.trayLoop(ctx) })
	}
}

// jsonError writes {"error": msg}, the shape the UI expects from the daemon.
func jsonError(w http.ResponseWriter, msg string, code int) {
	raw, _ := json.Marshal(map[string]string{"error": msg})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		if err := daemon.Run(log); errors.Is(err, daemon.ErrAlreadyRunning) {
			// exit 0, so launchd doesn't keep restarting us
			fmt.Fprintln(os.Stderr, "modalfs: another daemon is already running; this one is not needed")
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "modalfs:", err)
			os.Exit(1)
		}
		return
	}
	c := daemon.NewClient()
	app := &App{client: c}
	theApp = app
	daemonErr := c.Ensure() // spawns `<this binary> daemon` when needed

	target, _ := url.Parse("http://modalfsd")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.Transport()
	var ensureMu sync.Mutex // one restart at a time, not one per failed request
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// The daemon quit on purpose (`modalfs quit`, or a logout): the app
		// goes too, rather than mounting everything again.
		if _, serr := os.Stat(daemon.InfoPath()); errors.Is(serr, fs.ErrNotExist) && app.ran.Load() && !app.quitting.Load() && app.ctx != nil {
			app.quitting.Store(true)
			go wruntime.Quit(app.ctx)
		}
		// Otherwise it crashed: bring it back, unless this window is quitting it.
		if !app.quitting.Load() {
			ensureMu.Lock()
			ok := !app.quitting.Load() && c.Ensure() == nil
			app.ran.Store(app.ran.Load() || ok)
			ensureMu.Unlock()
			if ok {
				jsonError(w, "modalfs restarted, try again", http.StatusServiceUnavailable)
				return
			}
		}
		jsonError(w, err.Error(), http.StatusBadGateway)
	}
	if daemonErr != nil {
		slog.Error("could not start daemon", "err", daemonErr)
	}
	app.ran.Store(daemonErr == nil)

	err := wails.Run(&options.App{
		Title:            "modalfs",
		Width:            1000,
		Height:           680,
		MinWidth:         680,
		MinHeight:        480,
		BackgroundColour: background(),
		AssetServer:      &assetserver.Options{Assets: webui.FS(), Handler: proxy},
		Bind:             []interface{}{app},
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnBeforeClose:    app.beforeClose,
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About:                &mac.AboutInfo{Title: "modalfs", Message: "Modal Volumes as local folders.\nVersion " + daemon.VersionBuild()},
		},
		Linux: &linux.Options{Icon: icon, ProgramName: "modalfs", WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
		// The WebView's own menu (Copy, Paste…) on text fields and selected
		// text; elsewhere the UI shows its own, such as a volume's actions.
		EnableDefaultContextMenu: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "modalfs-gui:", err, runtime.GOOS)
		os.Exit(1)
	}
}

// On macOS the window is see-through and the UI draws its own translucent
// layers over the material; elsewhere there's nothing behind it, so it starts
// with the UI's solid light background.
func background() *options.RGBA {
	if runtime.GOOS == "darwin" {
		return &options.RGBA{}
	}
	return &options.RGBA{R: 234, G: 236, B: 244, A: 255}
}
