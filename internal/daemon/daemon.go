// Package daemon is the long-running modalfs process. It owns every mount
// (the FUSE/NFS servers live inside it, so it must keep running while
// volumes are mounted) and exposes a small JSON API over a Unix socket for
// the CLI and the GUI. Closing the GUI never unmounts anything.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/vfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var Version = "0.1.0"

type Event struct {
	Seq     int64    `json:"seq"`
	At      int64    `json:"at"`
	Kind    string   `json:"kind"` // mounted unmounted commit conflict error recovered info
	MountID string   `json:"mount_id,omitempty"`
	Volume  string   `json:"volume,omitempty"`
	Message string   `json:"message"`
	Code    string   `json:"code,omitempty"` // for localized display
	Args    []string `json:"args,omitempty"`
}

type Daemon struct {
	store *config.Store
	log   *slog.Logger
	cache *vfs.BlockCache

	mu      sync.Mutex
	running map[string]*runMount
	events  []Event
	seq     int64
	closing bool // shutting down: no new mounts

	rtMu sync.Mutex // serializes runtime.json writes

	clients clientCache // shared Modal clients for API calls
	dl      *dlManager  // remote downloads

	traffic   traffic     // each mount's totals over the last hour
	conflicts conflicts   // conflicts not marked handled yet, by mount
	usage     *usageStore // volume sizes, from listing them
	avatars   *avatarStore

	bootID  string
	auth    *uiAuth
	uiURL   string // the browser UI, without a token
	quit    chan struct{}
	once    sync.Once
	now     chan struct{} // quit without waiting for uploads
	nowOnce sync.Once
}

// ErrAlreadyRunning means another daemon holds the lock. It is not a failure:
// callers exit 0, so launchd's KeepAlive doesn't restart them forever.
var ErrAlreadyRunning = errors.New("another modalfs daemon is already running")

func SocketPath() string { return filepath.Join(config.Dir(), "modalfsd.sock") }
func InfoPath() string   { return filepath.Join(config.Dir(), "daemon.json") }
func LogPath() string    { return filepath.Join(config.Dir(), "daemon.log") }

// Info is written to daemon.json for clients.
type Info struct {
	PID     int    `json:"pid"`
	Socket  string `json:"socket"`
	UIURL   string `json:"ui_url"` // without a token: ask /api/ui/token for one
	Version string `json:"version"`
}

// Shutdown starts quitting: every mount is unmounted once its uploads are
// done. now quits without waiting for them; it also cuts short a shutdown
// that is already running.
func (d *Daemon) Shutdown(now bool) {
	if now {
		d.nowOnce.Do(func() {
			d.emitC("info", "quit_now", "", "", "quitting without waiting for uploads; what's left is uploaded the next time each volume is mounted")
			close(d.now)
		})
	}
	d.once.Do(func() { close(d.quit) })
}

func (d *Daemon) quittingNow() bool {
	select {
	case <-d.now:
		return true
	default:
		return false
	}
}

func (d *Daemon) emitC(kind, code, mountID, volume, msg string, args ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.events = append(d.events, Event{Seq: d.seq, At: time.Now().Unix(), Kind: kind, MountID: mountID, Volume: volume, Message: msg, Code: code, Args: args})
	if len(d.events) > 500 {
		d.events = d.events[len(d.events)-500:]
	}
	if kind != "commit" {
		d.log.Info("event", "kind", kind, "volume", volume, "msg", msg)
	}
}

// Run starts the daemon and blocks until it is told to stop.
func Run(log *slog.Logger) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(config.Dir(), "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return ErrAlreadyRunning
	}
	defer lock.Close()
	if err := checkSocketPath(SocketPath()); err != nil {
		return err
	}

	store, err := config.Open()
	if err != nil {
		return err
	}
	s := store.Snapshot().Settings
	cache, err := vfs.NewBlockCache(filepath.Join(s.CacheDir, "blocks"), int64(s.CacheSizeGB)<<30)
	if err != nil {
		return err
	}
	d := &Daemon{store: store, log: log, cache: cache, running: map[string]*runMount{}, bootID: randomHex(8), quit: make(chan struct{}), now: make(chan struct{})}
	if d.dl, err = newDLManager(d, config.Dir(), d.quit); err != nil {
		return err
	}
	d.usage = newUsageStore(config.Dir())
	d.avatars = newAvatarStore(config.Dir())
	d.cleanupStale()
	d.checkService()

	_ = os.Remove(SocketPath())
	ul, err := net.Listen("unix", SocketPath())
	if err != nil {
		return err
	}
	_ = os.Chmod(SocketPath(), 0o600)
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := tl.Addr().(*net.TCPAddr).Port
	d.uiURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	d.auth = newUIAuth(port)

	api := d.routes()
	go func() { _ = http.Serve(ul, api) }()
	go func() { _ = http.Serve(tl, d.tcpGuard(port, d.auth, api)) }()
	raw, _ := json.MarshalIndent(Info{PID: os.Getpid(), Socket: SocketPath(), UIURL: d.uiURL, Version: Version}, "", "  ")
	_ = os.WriteFile(InfoPath(), raw, 0o600)
	log.Info("modalfs daemon running", "socket", SocketPath(), "ui", d.uiURL, "version", Version)
	d.emitC("info", "started", "", "", "daemon started")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.watch(ctx)
	go d.sampleTraffic(ctx)
	go d.autoMount()
	d.dl.resume()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	select {
	case <-sig:
		d.Shutdown(false)
	case <-d.quit:
	}
	// A second signal (Ctrl-C again) quits without waiting for uploads.
	go func() {
		<-sig
		d.Shutdown(true)
	}()
	log.Info("shutting down: committing pending writes and unmounting")
	// Gone first: a desktop app that finds the daemon unreachable without
	// this file knows it quit on purpose and doesn't start it again.
	_ = os.Remove(InfoPath())
	d.stopAll()
	_ = ul.Close()
	_ = tl.Close()
	_ = os.Remove(SocketPath())
	// Removes runtime.json when every unmount succeeded; otherwise it keeps
	// what's left for the next start to clean up.
	d.saveRuntime()
	return nil
}

// checkSocketPath turns the kernel's cryptic "bind: invalid argument" for a
// too-long socket path into a message that says what to do.
func checkSocketPath(p string) error {
	limit := 104 // sun_path on macOS and the BSDs, including the final NUL
	if runtime.GOOS == "linux" {
		limit = 108
	}
	if len(p) >= limit {
		return fmt.Errorf("the socket path %s is %d bytes, but Unix sockets allow at most %d on %s; set MODALFS_HOME to a shorter directory", p, len(p), limit-1, runtime.GOOS)
	}
	return nil
}

func (d *Daemon) autoMount() {
	for _, m := range d.store.Snapshot().Mounts {
		if m.AutoMount {
			go d.autoMountOne(m.ID, m.VolumeName)
		}
	}
}

// Auto-mounts run at login, often before the network is up, so a failure
// that may fix itself is retried with backoff for a while.
var (
	autoMountDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}
	autoMountFor    = 30 * time.Minute
)

func (d *Daemon) autoMountOne(id, label string) {
	deadline := time.Now().Add(autoMountFor)
	for attempt := 0; ; attempt++ {
		ph, err := d.claimStart(id)
		if err != nil || ph == nil {
			return // shutting down, removed, or already mounted or mounting
		}
		err = d.runStart(id, ph, true)
		if err == nil {
			return
		}
		wait := autoMountDelays[min(attempt, len(autoMountDelays)-1)]
		if !mayFixItself(err) || time.Now().Add(wait).After(deadline) {
			msg := err.Error()
			if attempt > 0 {
				msg = fmt.Sprintf("%s (gave up after %d tries)", msg, attempt+1)
			}
			d.log.Warn("auto-mount failed", "volume", label, "err", err, "tries", attempt+1)
			d.emitC("error", "mount_failed", id, label, "mount failed: "+msg, msg)
			return
		}
		d.mu.Lock()
		if d.running[id] == ph {
			ph.retryAt = time.Now().Add(wait)
		}
		d.mu.Unlock()
		d.log.Info("auto-mount failed; will try again", "volume", label, "err", err, "in", wait)
		d.emitC("info", "automount_retry", id, label, fmt.Sprintf("auto-mount failed (%v); trying again in %s", err, wait), err.Error(), fmt.Sprint(int(wait.Seconds())))
		select {
		case <-time.After(wait):
		case <-d.quit:
			return
		}
		// Stop if the user dealt with it meanwhile: cleared the error,
		// started it by hand, removed the mount or turned auto-mount off.
		d.mu.Lock()
		still := d.running[id] == ph && ph.state == StateError
		d.mu.Unlock()
		m, ok := d.store.Mount(id)
		if !still || !ok || !m.AutoMount {
			return
		}
	}
}

// mayFixItself reports whether a mount failure looks like the network or
// Modal being unavailable, rather than something a retry can't change.
func mayFixItself(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
			return true
		}
		return false
	}
	// gRPC errors wrapped with %v lose their type; go by the text.
	s := err.Error()
	for _, k := range []string{"Unavailable", "DeadlineExceeded", "connection refused", "no such host", "network is unreachable", "i/o timeout"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// stopAll unmounts everything for quitting. Each mount waits for its
// uploads, then unmounts cleanly, retrying for a few seconds and forcing
// it as a last resort. Shutdown(true) stops waiting: what's still
// uploading stays journaled for the next mount.
func (d *Daemon) stopAll() {
	d.mu.Lock()
	d.closing = true
	d.mu.Unlock()
	// Let mounts that are still starting finish, so none is left behind.
	for i := 0; i < 1200 && d.anyStarting() && !d.quittingNow(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, id := range d.runningIDs() {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				// A stop the user started may still be uploading; let it finish.
				for d.stoppingNotClosing(id) {
					select {
					case <-time.After(500 * time.Millisecond):
					case <-d.now:
						return
					}
				}
				if err := d.stopMount(id, stopOpts{patient: true, force: true, quit: true}); err != nil {
					d.log.Error("unmount failed", "mount", id, "err", err)
				}
			}(id)
		}
		wg.Wait()
		// Mounts already unmounted with "unmount now" finish their uploads.
		for d.anyClosing() {
			time.Sleep(200 * time.Millisecond)
		}
	}()
	select {
	case <-done:
		return
	case <-d.now:
	}
	// Quit now: unmount what's still mounted and don't wait for uploads.
	var wg sync.WaitGroup
	for _, id := range d.runningIDs() {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := d.stopMount(id, stopOpts{now: true, force: true, quit: true}); err != nil {
				d.log.Error("unmount failed", "mount", id, "err", err)
			}
		}(id)
	}
	wg.Wait()
	for _, id := range d.runningIDs() {
		d.log.Warn("quitting before every upload finished; the rest is uploaded the next time it's mounted", "mount", id)
	}
}

func (d *Daemon) stoppingNotClosing(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.running[id]
	return r != nil && r.state == StateStopping && !r.closing
}

func (d *Daemon) anyClosing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.running {
		if r.closing {
			return true
		}
	}
	return false
}

func (d *Daemon) runningIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for id, r := range d.running {
		if r.state != StateError {
			ids = append(ids, id)
		}
	}
	return ids
}

func (d *Daemon) anyStarting() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.running {
		if r.state == StateStarting {
			return true
		}
	}
	return false
}

// ---------- views ----------

type StateView struct {
	Version   string           `json:"version"`
	Platform  string           `json:"platform"`
	Accounts  []config.Account `json:"accounts"`
	Mounts    []MountView      `json:"mounts"`
	Downloads []DownloadView   `json:"downloads"`
	Settings  config.Settings  `json:"settings"`
	Cache     CacheView        `json:"cache"`
	Service   ServiceView      `json:"service"`
	LastSeq   int64            `json:"last_seq"`
	Quitting  bool             `json:"quitting,omitempty"` // shutting down: unmounting everything
	BootID    string           `json:"boot_id"`            // new for every daemon process; event numbers restart with it
	ConfigDir string           `json:"config_dir"`
}

type CacheView struct {
	Blocks int   `json:"blocks"`
	Bytes  int64 `json:"bytes"`
	Max    int64 `json:"max"`
}

func (d *Daemon) State() StateView {
	f := d.store.Snapshot()
	n, sz := d.cache.Stats()
	v := StateView{Version: Version, Platform: platform(), Accounts: f.Accounts, Settings: f.Settings, BootID: d.bootID,
		Cache: CacheView{Blocks: n, Bytes: sz, Max: d.cache.Max()}, Service: serviceStatus(), ConfigDir: config.Dir()}
	if v.Accounts == nil {
		v.Accounts = []config.Account{}
	}
	accts := map[string]config.Account{}
	for _, a := range f.Accounts {
		accts[a.ID] = a
	}
	// Copy what we need while holding the lock: setState, StopMount and
	// friends change these fields under d.mu.
	type runCopy struct {
		state, err, mode, step, unmountErr string
		startedAt, retryAt, stopSince      time.Time
		stopBytesUp, stopPending           int64
		closing                            bool
		inUseBy                            []string
		fs                                 *vfs.FS
	}
	d.mu.Lock()
	v.LastSeq = d.seq
	v.Quitting = d.closing
	runs := map[string]runCopy{}
	for k, r := range d.running {
		runs[k] = runCopy{r.state, r.err, r.mode, r.step, r.unmountErr, r.startedAt, r.retryAt, r.stopSince,
			r.stopBytesUp, r.stopPending, r.closing, r.inUseBy, r.fs}
	}
	d.mu.Unlock()
	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	v.Downloads = d.dl.views()
	v.Mounts = []MountView{}
	for _, m := range f.Mounts {
		mv := MountView{Mount: m, State: StateStopped, AccountLabel: accts[m.AccountID].Label, Workspace: accts[m.AccountID].Workspace}
		mv.OpenConflicts = d.conflicts.count(m.ID)
		if d.usage != nil && m.VolumeID != "" {
			if u, ok := d.usage.last(m.VolumeID); ok {
				mv.SizeBytes, mv.SizeAt = u.Total, u.At
			}
		}
		if r, ok := runs[m.ID]; ok {
			mv.State, mv.Error, mv.ActiveMode = r.state, r.err, r.mode
			mv.StartedAt, mv.Step = unix(r.startedAt), r.step
			mv.UnmountError, mv.InUseBy = r.unmountErr, r.inUseBy
			if r.state == StateStopping {
				mv.StoppingSince, mv.StopBytesUp, mv.StopPendingBytes, mv.Unmounted = unix(r.stopSince), r.stopBytesUp, max(r.stopPending, 0), r.closing
			}
			if r.state == StateError {
				mv.RetryAt = unix(r.retryAt)
			}
			if r.fs != nil && (r.state == StateMounted || r.state == StateStopping) {
				st := r.fs.Status()
				mv.Status = &st
			}
		}
		v.Mounts = append(v.Mounts, mv)
	}
	sort.SliceStable(v.Mounts, func(i, j int) bool { return v.Mounts[i].VolumeName < v.Mounts[j].VolumeName })
	return v
}

func (d *Daemon) Events(after int64) []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []Event{}
	for _, e := range d.events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}
