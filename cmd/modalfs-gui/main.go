// modalfs-gui is the desktop window (Wails: the system WebView, so the app
// stays small). It is only a client: mounts live in the background daemon,
// which this binary can also run (`modalfs-gui daemon`), so closing the
// window never unmounts anything.
//
// Build with `just app` (macOS, needs the Xcode command line tools) or
// `just linux-gui`. By hand:
//
//	go build -tags desktop,production,private_mac_apis -ldflags "-s -w" -o modalfs-gui ./cmd/modalfs-gui
//
// macOS needs private_mac_apis for its transparent WebView. Linux builds
// omit that tag and need GTK4 and WebKitGTK 6.0.
package main

import (
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
	"github.com/t41372/modalfs/internal/webui"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed icon.png
var icon []byte

// App holds the window's state and the methods the UI can call as a Wails service.
type App struct {
	desktop  *application.App
	window   *application.WebviewWindow
	client   *daemon.Client
	quitting atomic.Bool // the window may close for good now
	closing  atomic.Bool // without a menu bar icon: the window may close, the daemon stays
	asking   atomic.Bool // a quit or close request is waiting for the UI
	acked    atomic.Bool // the UI answered it
	ran      atomic.Bool // a daemon was running for this window at some point
	quitOnce sync.Once   // starts quitAfterDaemon
}

// theApp is the one App, for the callbacks from native_darwin.m.
var theApp *App

// SetAppearance makes the window light, dark, or follow the system ("system"),
// so the glass behind the UI matches the appearance chosen in Settings.
func (*App) SetAppearance(name string) { setAppearance(name) }

// Quitting is called by the UI right before it posts /api/shutdown. From then
// on a failed request never starts a new daemon, which would mount again, and
// the app quits once the daemon is gone. The app watches for that itself: the
// page in a hidden window stops polling, so it would never notice.
func (a *App) Quitting() {
	a.quitting.Store(true)
	a.quitOnce.Do(func() { go a.quitAfterDaemon() })
}

// quitAfterDaemon quits once the daemon stops answering its ping (which takes
// no locks, so a daemon that can't answer is exiting), after a moment for
// the window to show that the quit is done.
func (a *App) quitAfterDaemon() {
	for a.client.Alive() {
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(700 * time.Millisecond)
	a.desktop.Quit()
}

// QuitAck tells the app the UI is showing the quit checklist it asked for.
func (a *App) QuitAck() { a.acked.Store(true) }

// CloseAck tells the app the UI got the close request (no menu bar icon).
func (a *App) CloseAck() { a.acked.Store(true) }

// CloseWindow closes the window without quitting: the daemon keeps the mounts.
func (a *App) CloseWindow() {
	a.closing.Store(true)
	go a.desktop.Quit()
}

// SaveText asks where to save a text file, such as an encrypted volume's key
// (the WebView can't download), and writes it readable only by its owner. It
// returns the path, or "" when the person cancelled.
func (a *App) SaveText(name, text string) (string, error) {
	path, err := a.desktop.Dialog.SaveFile().SetFilename(name).CanCreateDirectories(true).AttachToWindow(a.window).PromptForSingleSelection()
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
	if a.quitting.Load() || a.desktop == nil {
		return
	}
	nativeShowWindow()
	a.ask("quit:request", func() {
		a.quitting.Store(true)
		a.desktop.Quit()
	})
}

// requestClose asks the UI before the window closes, so it can say that the
// volumes stay mounted and offer to quit instead. A window that doesn't
// answer (its page failed to load) just closes.
func (a *App) requestClose() {
	a.window.UnMinimise()
	a.window.Show()
	a.ask("close:request", a.CloseWindow)
}

// ask sends the UI a request and waits for its answer (QuitAck, CloseAck);
// without one in 4 s, fallback runs. A request while one is waiting is
// dropped, since the UI is about to answer it: resetting the answer would
// make the first one time out under the dialog it opened.
func (a *App) ask(event string, fallback func()) {
	if !a.asking.CompareAndSwap(false, true) {
		return
	}
	defer a.asking.Store(false)
	a.acked.Store(false)
	a.desktop.Event.Emit(event)
	for range 40 {
		if a.acked.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !a.acked.Load() && !a.quitting.Load() {
		fallback()
	}
}

// beforeQuit asks the UI to finish the quit checklist. A logout quits at
// once; launchd signals the separate daemon, which handles its own unmounts.
func (a *App) beforeQuit() bool {
	if a.quitting.Load() || a.closing.Load() {
		return true
	}
	if nativeIsSystemQuit() {
		a.quitting.Store(true)
		return true
	}
	go a.requestQuit()
	return false
}

// Closing the macOS window leaves the menu bar app. Without a menu bar
// icon, the UI explains that the separate daemon keeps the mounts first.
// Only the app shutting down closes the window for good; until then it can
// be shown again, in the middle of a quit too.
func (a *App) beforeClose(event *application.WindowEvent) {
	if a.desktop.Context().Err() != nil {
		return
	}
	event.Cancel()
	if hasTray {
		nativeHideWindow()
		return
	}
	go a.requestClose()
}

// nativeAction is a click in the menu bar menu or the app menu.
func (a *App) nativeAction(action, id string) {
	nav := func(section, id string) {
		nativeShowWindow()
		a.desktop.Event.Emit("nav", section, id)
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

// started sets up the menu bar icon and the app menus. They show the
// daemon's state, not the page's, so they're there even if the page fails.
func (a *App) started() {
	if hasTray {
		nativeInit()
		go a.trayLoop(a.desktop.Context())
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
		if _, serr := os.Stat(daemon.InfoPath()); errors.Is(serr, fs.ErrNotExist) && app.ran.Load() && !app.quitting.Load() && app.desktop != nil {
			app.quitting.Store(true)
			go app.desktop.Quit()
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

	nativeFS, err := webui.DesktopFS(webui.FS())
	if err != nil {
		fmt.Fprintln(os.Stderr, "modalfs-gui:", err)
		os.Exit(1)
	}
	assets := application.BundledAssetFileServer(nativeFS)
	api := webui.DesktopAPI(proxy)
	app.desktop = application.New(application.Options{
		Name:        "modalfs",
		Description: "Modal Volumes as local folders.\nVersion " + daemon.VersionBuild(),
		Icon:        icon,
		Services:    []application.Service{application.NewService(app)},
		ShouldQuit:  app.beforeQuit,
		// Wails turns SIGTERM and Ctrl-C into a quit request, which shows the
		// quit checklist and waits for someone to answer it, then ignores
		// every later signal. A signal (a Linux logout, kill) just ends the
		// app, as a macOS logout does; the separate daemon keeps the mounts.
		DisableDefaultSignalHandler: true,
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				api.ServeHTTP(w, r)
				return
			}
			assets.ServeHTTP(w, r)
		})},
		Linux: application.LinuxOptions{ProgramName: "modalfs", ApplicationID: "dev.modalfs.app"},
	})
	app.window = app.desktop.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "modalfs",
		Width: 1000, Height: 680, MinWidth: 680, MinHeight: 480,
		BackgroundColour: background(),
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
			Backdrop: application.MacBackdropTranslucent,
		},
		Linux: application.LinuxWindow{Icon: icon, WebviewGpuPolicy: application.WebviewGpuPolicyOnDemand},
	})
	app.window.RegisterHook(events.Common.WindowClosing, app.beforeClose)
	app.window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) { styleWindow() })
	app.desktop.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { app.started() })
	if hasTray {
		app.desktop.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { nativeShowWindow() })
	}
	if err := app.desktop.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "modalfs-gui:", err, runtime.GOOS)
		os.Exit(1)
	}
}

// On macOS the window is see-through and the UI draws its own translucent
// layers over the material; elsewhere there's nothing behind it, so it starts
// with the UI's solid light background.
func background() application.RGBA {
	if runtime.GOOS == "darwin" {
		return application.RGBA{}
	}
	return application.RGBA{Red: 234, Green: 236, Blue: 244, Alpha: 255}
}
