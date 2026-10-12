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
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/t41372/modalfs/internal/daemon"
	"github.com/t41372/modalfs/internal/webui"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed icon.png
var icon []byte

// App holds the methods the UI can call (window.go.main.App.*).
type App struct {
	quitting atomic.Bool
}

// SetAppearance makes the window light, dark, or follow the system ("system"),
// so the glass behind the UI matches the appearance chosen in Settings.
func (*App) SetAppearance(name string) { setAppearance(name) }

// Quitting is called by the UI right before it posts /api/shutdown. From then
// on a failed request never starts a new daemon, which would mount again.
func (a *App) Quitting() { a.quitting.Store(true) }

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
	app := &App{}
	c := daemon.NewClient()
	daemonErr := c.Ensure() // spawns `<this binary> daemon` when needed

	target, _ := url.Parse("http://modalfsd")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.Transport()
	var ensureMu sync.Mutex // one restart at a time, not one per failed request
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// the daemon may have been quit from the CLI; try to bring it back,
		// unless this window is quitting it
		if !app.quitting.Load() {
			ensureMu.Lock()
			ok := !app.quitting.Load() && c.Ensure() == nil
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

	err := wails.Run(&options.App{
		Title:            "modalfs",
		Width:            1000,
		Height:           680,
		MinWidth:         680,
		MinHeight:        480,
		BackgroundColour: background(),
		AssetServer:      &assetserver.Options{Assets: webui.FS(), Handler: proxy},
		Bind:             []interface{}{app},
		OnDomReady:       func(context.Context) { styleWindow() },
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About:                &mac.AboutInfo{Title: "modalfs", Message: "Modal Volumes as local folders.\nVersion " + daemon.Version},
		},
		Linux: &linux.Options{Icon: icon, ProgramName: "modalfs", WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
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
